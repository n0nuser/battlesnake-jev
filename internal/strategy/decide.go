package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/n0nuser/battlesnake-jev/internal/api"
	"github.com/n0nuser/battlesnake-jev/internal/game"
	"github.com/n0nuser/battlesnake-jev/internal/jev"
)

// Config tunes the decision ladder.
type Config struct {
	// TieBreak enables the in-turn inference call. Turning it off leaves the
	// background posture in place and is the documented degraded mode when
	// in-turn calls cannot meet the deadline.
	TieBreak bool
	// Margin is held back from the turn budget to cover the trip home to the
	// game engine, JSON encoding and scheduling jitter.
	Margin time.Duration
	// CloseEnough is the score gap below which the top two moves are treated
	// as a genuine tie worth asking about. A clear winner is never asked.
	CloseEnough float64
	// LowHealth is the health at which finding food overrides the posture.
	LowHealth int
	// BoardInState draws the board into the inference state. Without it the
	// model only sees the numbers the scorer already computed, and can only
	// re-rank them; with it, it sees the shape those numbers flatten away.
	BoardInState bool
	// AlwaysAsk consults inference on every turn that has a real choice,
	// skipping the cheap-path gates. It is for measuring what the model
	// contributes, not for ordinary play: it spends roughly five times the
	// tokens and still cannot beat the turn deadline any more often.
	AlwaysAsk bool
}

// Penalties for the two head-to-head outcomes that kill us. They are close
// together because both are fatal; a loss is only fractionally worse in that it
// leaves the rival alive.
const (
	tiePenalty  = 0.95
	losePenalty = 1.00
	// crowdPenalty is added for each rival beyond the first that can contest
	// the square, so a standoff against one snake outranks one against three.
	crowdPenalty = 0.15
)

// DefaultConfig is tuned from the measured warm latency of the inference API.
func DefaultConfig() Config {
	return Config{
		TieBreak:    true,
		Margin:      120 * time.Millisecond,
		CloseEnough: 0.12,
		LowHealth:   30,
	}
}

// Reason records why a move was chosen, so logs explain the token spend.
type Reason string

// The ways a move can be reached, from cheapest to most expensive.
const (
	// ReasonTrapped means no move was safe and we picked the least bad one.
	ReasonTrapped Reason = "trapped"
	// ReasonOnlyMove means exactly one move was safe.
	ReasonOnlyMove Reason = "only-move"
	// ReasonClearWinner means the deterministic scores were decisive.
	ReasonClearWinner Reason = "clear-winner"
	// ReasonInterchangeable means the close options were also identical in
	// every measured way, so there was nothing for a judgment to weigh.
	ReasonInterchangeable Reason = "interchangeable"
	// ReasonNoBudget means the turn could not afford an inference call.
	ReasonNoBudget Reason = "no-budget"
	// ReasonColdPool means the connection was cold, so a call was skipped.
	ReasonColdPool Reason = "cold-pool"
	// ReasonTieBreakDisabled means in-turn inference is switched off.
	ReasonTieBreakDisabled Reason = "tiebreak-disabled"
	// ReasonJev means inference picked between close deterministic options.
	ReasonJev Reason = "jev"
	// ReasonJevFailed means inference was asked but the fallback was used.
	ReasonJevFailed Reason = "jev-failed"
)

// Decision is the chosen move plus everything worth logging about it.
type Decision struct {
	Move       string
	Reason     Reason
	Mode       Mode
	Candidates []Candidate
	Confidence float64
	Latency    time.Duration
	Tokens     int
}

// Candidate is one scored move.
type Candidate struct {
	Dir        game.Direction
	Space      int
	FoodDist   int
	HasFood    bool
	TailSafe   bool
	H2H        game.H2HRisk
	Contesters int
	Hazard     bool
	Score      float64
}

// Decider turns board states into moves.
type Decider struct {
	asker jev.Asker
	cold  func() bool
	warm  func(context.Context) error
	cfg   Config
	log   *slog.Logger

	// warming keeps at most one warm-up in flight at a time.
	warming atomic.Bool
}

// NewDecider builds a Decider. A nil asker disables inference entirely, which
// keeps the deterministic engine fully usable and testable on its own.
func NewDecider(asker jev.Asker, cfg Config, log *slog.Logger) *Decider {
	if log == nil {
		log = slog.Default()
	}
	d := &Decider{asker: asker, cfg: cfg, log: log, cold: func() bool { return false }}
	if c, ok := asker.(interface{ LikelyCold() bool }); ok {
		d.cold = c.LikelyCold
	}
	if c, ok := asker.(interface{ Warm(context.Context) error }); ok {
		d.warm = c.Warm
	}
	return d
}

// warmInBackground pays the TLS handshake off the critical path. The turn does
// not wait for it, and only one warm-up runs at a time. This is what lets a
// pool that went cold - or whose warm-up at game start failed - recover on its
// own instead of skipping inference for the rest of the game.
func (d *Decider) warmInBackground(gameID string) {
	if d.warm == nil || !d.warming.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer d.warming.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.warm(ctx); err != nil {
			d.log.Warn("connection warm-up failed", "game", gameID, "err", err)
		}
	}()
}

// Decide returns the move to play. It always returns a legal direction, even
// when every option loses and even when everything about inference fails.
func (d *Decider) Decide(ctx context.Context, req api.GameRequest, gs *GameState) Decision {
	grid := game.Occupancy(req.Board)
	safe := game.SafeMoves(grid, req.You)
	mode := gs.Mode()

	// The posture for later turns is refreshed off the critical path.
	d.maybeRefreshMode(req, gs)

	if len(safe) == 0 {
		return Decision{Move: d.leastBad(grid, req).String(), Reason: ReasonTrapped, Mode: mode}
	}

	cands := d.score(grid, req, mode, safe)
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Score > cands[j].Score })
	best := cands[0]

	decide := func(r Reason) Decision {
		return Decision{Move: best.Dir.String(), Reason: r, Mode: mode, Candidates: cands}
	}

	if len(cands) == 1 {
		return decide(ReasonOnlyMove)
	}
	if !d.cfg.AlwaysAsk && cands[0].Score-cands[1].Score > d.cfg.CloseEnough {
		return decide(ReasonClearWinner)
	}
	// A close score is not on its own a reason to ask. On an open board the
	// leading options are often identical in every measured way - same space,
	// same tail access, same risk, same distance to food - and a judgment
	// between interchangeable options buys nothing for the tokens it costs.
	// Inference is worth paying for only when the options genuinely trade off.
	if !d.cfg.AlwaysAsk && features(cands[0]) == features(cands[1]) {
		return decide(ReasonInterchangeable)
	}
	if !d.cfg.TieBreak || d.asker == nil {
		return decide(ReasonTieBreakDisabled)
	}
	if d.cold() {
		d.warmInBackground(gs.ID)
		return decide(ReasonColdPool)
	}

	budget, ok := remainingBudget(ctx, d.cfg.Margin)
	if !ok || budget < gs.EstimatedLatency() {
		return decide(ReasonNoBudget)
	}

	askCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	resp, err := d.asker.Ask(askCtx, tieBreakRequest(req, mode, cands, d.cfg.BoardInState))
	if err != nil {
		gs.fallbacks.Add(1)
		d.log.Warn("tie-break inference failed, using deterministic move",
			"game", gs.ID, "turn", req.Turn, "move", best.Dir, "err", err)
		out := decide(ReasonJevFailed)
		return out
	}

	gs.calls.Add(1)
	gs.inputTokens.Add(int64(resp.Usage.InputTokens))
	gs.observeLatency(resp.Latency)

	answer, ok := resp.Answers["move"]
	if !ok {
		gs.fallbacks.Add(1)
		return decide(ReasonJevFailed)
	}
	chosen, ok := matchCandidate(answer.Choice, cands)
	if !ok {
		// An answer outside the offered set is not trusted. This cannot happen
		// with a well-formed reply, and if it does the deterministic move wins.
		gs.fallbacks.Add(1)
		d.log.Warn("inference returned a move outside the safe set",
			"game", gs.ID, "turn", req.Turn, "answer", answer.Choice)
		return decide(ReasonJevFailed)
	}

	if chosen.Dir != best.Dir {
		gs.overrides.Add(1)
	}

	return Decision{
		Move:       chosen.Dir.String(),
		Reason:     ReasonJev,
		Mode:       mode,
		Candidates: cands,
		Confidence: answer.Confidence,
		Latency:    resp.Latency,
		Tokens:     resp.Usage.InputTokens,
	}
}

// score rates every safe move. Spatial reasoning stays here: a flood fill is
// exact and free, and asking a model to do it would be both slower and worse.
func (d *Decider) score(grid *game.Grid, req api.GameRequest, mode Mode, safe []game.Direction) []Candidate {
	w := weights(mode, req.You.Health, d.cfg.LowHealth)
	tail := req.You.Body[len(req.You.Body)-1]
	maxEdge := (min(req.Board.Width, req.Board.Height) - 1) / 2

	cands := make([]Candidate, 0, len(safe))
	for _, dir := range safe {
		target := dir.Apply(req.You.Head)
		space, tailSafe := game.FloodReach(grid, target, tail)
		foodDist, hasFood := game.NearestFoodDistance(target, req.Board.Food)
		risk, contesters := game.HeadToHead(target, req.Board, req.You)
		c := Candidate{
			Dir:        dir,
			Space:      space,
			FoodDist:   foodDist,
			HasFood:    hasFood,
			TailSafe:   tailSafe,
			H2H:        risk,
			Contesters: contesters,
			Hazard:     game.HazardAt(target, req.Board.Hazards),
		}

		// Space is measured against our own length: a pocket smaller than the
		// snake is a delayed self-collision, not merely a cramped square.
		//
		// The ratio is space/(space+length) rather than a capped space/length,
		// because a capped term saturates at 1 for every roomy option and made
		// the scorer flat exactly when it needed to discriminate. This form
		// rises steeply while space is scarce and keeps separating options
		// once it is plentiful.
		room := float64(space) / float64(space+req.You.Length+1)
		c.Score = w.space * room
		if space <= req.You.Length {
			c.Score -= 0.50
		}

		// Being able to path back to our own tail is what turns a long game
		// into a survivable one, so it is weighted like a first-class signal.
		if tailSafe {
			c.Score += w.tail
		}

		if hasFood {
			c.Score += w.food / float64(1+foodDist)
		}
		switch c.H2H {
		case game.H2HWin:
			c.Score += w.hunt
		case game.H2HTie:
			// A tie eliminates both snakes, so it kills us exactly as dead as
			// a loss does; the only difference is that we take the rival with
			// us. It is penalised almost as hard for that reason. This matters
			// most in the opening, when every snake is the same length and so
			// every head-to-head is a tie - which is what was wiping out two
			// snakes at a time around turn ten.
			c.Score -= tiePenalty + crowdPenalty*float64(c.Contesters-1)
		case game.H2HLose:
			c.Score -= losePenalty + crowdPenalty*float64(c.Contesters-1)
		case game.H2HNone:
		}
		if c.Hazard {
			c.Score -= 0.15
		}

		// A mild pull towards the middle. It only separates options that are
		// otherwise equal, which is most turns on an open board.
		if maxEdge > 0 {
			c.Score += 0.05 * float64(game.EdgeDistance(grid, target)) / float64(maxEdge)
		}

		cands = append(cands, c)
	}
	return cands
}

// featureSet is the comparable summary of what makes one option differ from
// another. Space and food distance are bucketed because a one-square
// difference is not a trade-off worth a round trip.
type featureSet struct {
	spaceBucket int
	foodBucket  int
	tailSafe    bool
	h2h         game.H2HRisk
	contesters  int
	hazard      bool
}

func features(c Candidate) featureSet {
	food := -1
	if c.HasFood {
		food = c.FoodDist / 3
	}
	return featureSet{
		spaceBucket: c.Space / 5,
		foodBucket:  food,
		tailSafe:    c.TailSafe,
		h2h:         c.H2H,
		contesters:  c.Contesters,
		hazard:      c.Hazard,
	}
}

type modeWeights struct {
	space float64
	food  float64
	hunt  float64
	tail  float64
}

// weights turns the posture into scoring weights. Low health overrides the
// posture: running out of health is a rule, not a judgment call, so it stays in
// code rather than being delegated.
func weights(mode Mode, health, lowHealth int) modeWeights {
	var w modeWeights
	switch mode {
	case ModeEat:
		w = modeWeights{space: 0.60, food: 0.80, hunt: 0.10, tail: 0.30}
	case ModeHunt:
		w = modeWeights{space: 0.70, food: 0.25, hunt: 0.35, tail: 0.35}
	case ModeTrap:
		w = modeWeights{space: 0.85, food: 0.20, hunt: 0.25, tail: 0.40}
	case ModeSurvive:
		w = modeWeights{space: 1.00, food: 0.15, hunt: 0.05, tail: 0.50}
	default:
		w = modeWeights{space: 1.00, food: 0.15, hunt: 0.05, tail: 0.50}
	}
	if health <= lowHealth {
		w.food += 0.60
		w.hunt = 0
	}
	return w
}

// leastBad is used when nothing is safe. It still prefers a square on the board
// and the largest pocket, because an opponent may vacate it as we arrive.
func (d *Decider) leastBad(grid *game.Grid, req api.GameRequest) game.Direction {
	best, bestSpace := game.Up, -1
	for _, dir := range game.Directions {
		target := dir.Apply(req.You.Head)
		if !grid.InBounds(target) {
			continue
		}
		space := game.FloodFill(grid, target)
		if space > bestSpace {
			best, bestSpace = dir, space
		}
	}
	return best
}

// remainingBudget derives how long an inference call may take from the
// request's own deadline, which the handler set from game.timeout.
func remainingBudget(ctx context.Context, margin time.Duration) (time.Duration, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, false
	}
	budget := time.Until(deadline) - margin
	if budget <= 0 {
		return 0, false
	}
	return budget, true
}

func matchCandidate(name string, cands []Candidate) (Candidate, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, c := range cands {
		if c.Dir.String() == name {
			return c, true
		}
	}
	return Candidate{}, false
}

// tieBreakRequest builds the inference call.
//
// Two things keep the token count down and the answer good. The raw move
// request is never sent: identifiers, customisations, shouts and latencies are
// dropped in favour of a computed digest. And the options offered are only the
// moves already proven safe, each labelled with its computed consequence, so
// the model judges trade-offs rather than redoing geometry.
func tieBreakRequest(req api.GameRequest, mode Mode, cands []Candidate, withBoard bool) jev.Request {
	criteria := make(map[string]string, len(cands))
	for _, c := range cands {
		criteria[c.Dir.String()] = describe(c, req.You.Length)
	}
	state := map[string]any{
		"turn":     req.Turn,
		"health":   req.You.Health,
		"length":   req.You.Length,
		"longest":  longestOpponent(req),
		"rivals":   len(req.Board.Snakes) - 1,
		"strategy": string(mode),
	}
	if withBoard {
		state["board"] = renderBoard(req.Board, req.You)
		state["legend"] = "H my head, # my body, E rival head, + rival body, o food, x hazard, . empty; top row is the far side of the board from y=0"
	}
	return jev.Request{
		State: state,
		Questions: map[string]jev.Question{
			"move": {
				Type: jev.TypeChoice,
				Instructions: "Pick the best move for my snake this turn. " +
					"Every option is already proven collision-safe, and the numbers " +
					"given are exact. Space is the squares reachable after the move, " +
					"food is the steps to the nearest food, and h2h is the outcome if " +
					"a rival moves into the same square. Weigh survival space against " +
					"health urgency and the strategy in the state.",
				Criteria: criteria,
			},
		},
	}
}

// describe renders one option's computed consequences compactly.
func describe(c Candidate, myLength int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "space %d", c.Space)
	if c.Space <= myLength {
		b.WriteString(" (smaller than my body)")
	}
	if c.TailSafe {
		b.WriteString(", tail reachable")
	} else {
		b.WriteString(", tail cut off")
	}
	if c.HasFood {
		fmt.Fprintf(&b, ", food %d", c.FoodDist)
	} else {
		b.WriteString(", no food")
	}
	switch c.H2H {
	case game.H2HWin:
		b.WriteString(", h2h win")
	case game.H2HTie:
		fmt.Fprintf(&b, ", h2h both die vs %d rival(s)", c.Contesters)
	case game.H2HLose:
		fmt.Fprintf(&b, ", h2h lose vs %d rival(s)", c.Contesters)
	case game.H2HNone:
		b.WriteString(", h2h none")
	}
	if c.Hazard {
		b.WriteString(", hazard")
	}
	return b.String()
}

func longestOpponent(req api.GameRequest) int {
	longest := 0
	for i := range req.Board.Snakes {
		s := &req.Board.Snakes[i]
		if s.ID == req.You.ID {
			continue
		}
		if s.Length > longest {
			longest = s.Length
		}
	}
	return longest
}
