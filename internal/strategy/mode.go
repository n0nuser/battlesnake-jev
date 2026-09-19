package strategy

import (
	"context"
	"strconv"
	"time"

	"github.com/n0nuser/battlesnake-jev/internal/api"
	"github.com/n0nuser/battlesnake-jev/internal/jev"
)

// modeRefreshTimeout bounds a background posture call. It is generous because
// the call is off the critical path; nothing waits on it.
const modeRefreshTimeout = 5 * time.Second

// minRefreshGap is the fewest turns between posture calls when only our own
// health has moved. Health oscillates as the snake eats, so a bare band change
// re-asked the same question dozens of times per game for the same answer. A
// rival being eliminated bypasses this floor, because that genuinely changes
// how the game should be played.
const minRefreshGap = 20

// maybeRefreshMode asks for a new posture when the situation has changed in a
// way that could plausibly change the answer.
//
// This is where the token budget is actually won. Asking every turn would spend
// hundreds of calls per game re-deriving a judgment that moves slowly, so the
// question is re-asked only when our health band, the number of rivals, or
// whether we are the longest snake has changed. The call runs in its own
// goroutine on a background context: the turn never waits for it, and its answer
// simply applies from the next turn onward.
func (d *Decider) maybeRefreshMode(req api.GameRequest, gs *GameState) {
	if d.asker == nil {
		return
	}

	current := fingerprint(req)

	gs.mu.Lock()
	unchanged := gs.fingerprint.initialised && gs.fingerprint == current
	rivalsChanged := gs.fingerprint.initialised && gs.fingerprint.opponentsAlive != current.opponentsAlive
	tooSoon := req.Turn-gs.lastRefresh < minRefreshGap
	if gs.refreshing || unchanged || (tooSoon && !rivalsChanged) {
		gs.fingerprint = current
		gs.mu.Unlock()
		return
	}
	gs.refreshing = true
	gs.fingerprint = current
	gs.lastRefresh = req.Turn
	gs.mu.Unlock()

	// Deliberately not derived from the request context: that context is
	// cancelled the moment we answer the turn, which is exactly when this call
	// still has work to do.
	ctx, cancel := context.WithTimeout(context.Background(), modeRefreshTimeout)

	go func() {
		defer cancel()
		defer func() {
			gs.mu.Lock()
			gs.refreshing = false
			gs.mu.Unlock()
		}()

		resp, err := d.askMode(ctx, req)
		if err != nil {
			d.log.Warn("posture refresh failed, keeping previous strategy",
				"game", gs.ID, "turn", req.Turn, "mode", gs.Mode(), "err", err)
			return
		}

		gs.calls.Add(1)
		gs.inputTokens.Add(int64(resp.Usage.InputTokens))

		answer, ok := resp.Answers["strategy"]
		if !ok {
			return
		}
		next := Mode(answer.Choice)
		if !validModes[next] {
			d.log.Warn("posture refresh returned an unknown strategy",
				"game", gs.ID, "answer", answer.Choice)
			return
		}
		gs.mode.Store(next)
		d.log.Info("strategy updated",
			"game", gs.ID, "turn", req.Turn, "mode", next,
			"confidence", answer.Confidence, "tokens", resp.Usage.InputTokens)
	}()
}

func (d *Decider) askMode(ctx context.Context, req api.GameRequest) (*jev.Response, error) {
	if c, ok := d.asker.(interface {
		AskWithRetry(context.Context, jev.Request, int) (*jev.Response, error)
	}); ok {
		return c.AskWithRetry(ctx, modeRequest(req), 3)
	}
	return d.asker.Ask(ctx, modeRequest(req))
}

// fingerprint is the coarse situation summary that gates a refresh. Health is
// bucketed so ordinary turn-by-turn decay does not trigger a call.
func fingerprint(req api.GameRequest) situation {
	longest := longestOpponent(req)
	return situation{
		healthBucket:   req.You.Health / 25,
		opponentsAlive: len(req.Board.Snakes) - 1,
		longest:        req.You.Length > longest,
		initialised:    true,
	}
}

// modeRequest asks for the standing posture. The state is a digest, not the
// board: the model is being asked how to play, not where to move.
func modeRequest(req api.GameRequest) jev.Request {
	return jev.Request{
		State: map[string]any{
			"turn":    req.Turn,
			"health":  req.You.Health,
			"length":  req.You.Length,
			"longest": longestOpponent(req),
			"rivals":  len(req.Board.Snakes) - 1,
			"board":   boardSize(req),
		},
		Questions: map[string]jev.Question{
			"strategy": {
				Type: jev.TypeChoice,
				Instructions: "Choose how my snake should play for the next stretch " +
					"of this Battlesnake game, given its health, its length relative " +
					"to the longest rival, and how many rivals are left.",
				Criteria: map[string]string{
					string(ModeSurvive): "Health and space are fine or the position is fragile; keep the most open space and take no risks.",
					string(ModeEat):     "Health is running low or we are shorter than a rival; go for food even through tighter space.",
					string(ModeHunt):    "We are longer than the rivals nearby; seek head-to-head collisions we would win.",
					string(ModeTrap):    "We are long and in control; cut rivals off from open space rather than chasing food.",
				},
			},
		},
	}
}

func boardSize(req api.GameRequest) string {
	return strconv.Itoa(req.Board.Width) + "x" + strconv.Itoa(req.Board.Height)
}
