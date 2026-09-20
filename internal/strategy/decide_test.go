package strategy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n0nuser/battlesnake-jev/internal/api"
	"github.com/n0nuser/battlesnake-jev/internal/game"
	"github.com/n0nuser/battlesnake-jev/internal/jev"
)

// fakeAsker stands in for the inference client. It is concurrency-safe because
// the background posture refresh calls it from its own goroutine.
type fakeAsker struct {
	mu      sync.Mutex
	calls   map[string]int
	answer  string
	err     error
	latency time.Duration
	block   chan struct{}
	scores  map[string]float64
	choices map[string]string
	conf    map[string]float64
}

func newFake(answer string) *fakeAsker {
	return &fakeAsker{calls: map[string]int{}, answer: answer}
}

func (f *fakeAsker) Ask(ctx context.Context, req jev.Request) (*jev.Response, error) {
	f.mu.Lock()
	for id := range req.Questions {
		f.calls[id]++
	}
	block, err, answer, latency := f.block, f.err, f.answer, f.latency
	f.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	scores, choices, conf := f.scores, f.choices, f.conf
	f.mu.Unlock()

	answers := make(map[string]jev.Answer, len(req.Questions))
	for id, q := range req.Questions {
		if q.Type == jev.TypeScore {
			v, ok := scores[id]
			if !ok {
				continue // a question the fake was not told how to answer
			}
			answers[id] = jev.Answer{Type: jev.TypeScore, Score: v, Confidence: 0.8}
			continue
		}
		pick, confidence := answer, 0.9
		if c, ok := choices[id]; ok {
			pick = c
		}
		if c, ok := conf[id]; ok {
			confidence = c
		}
		answers[id] = jev.Answer{Type: jev.TypeChoice, Choice: pick, Confidence: confidence}
	}
	return &jev.Response{
		Model:   "jev-test",
		Answers: answers,
		Usage:   jev.Usage{InputTokens: 400, OutputTokens: 38},
		Latency: latency,
	}, nil
}

func (f *fakeAsker) count(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[id]
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func coord(x, y int) api.Coord { return api.Coord{X: x, Y: y} }

func snake(id string, health int, body ...api.Coord) api.Battlesnake {
	return api.Battlesnake{ID: id, Health: health, Body: body, Head: body[0], Length: len(body)}
}

// request builds a move request on an 11x11 board with the given snakes, the
// first of which is us.
func request(snakes []api.Battlesnake, food []api.Coord) api.GameRequest {
	return api.GameRequest{
		Game:  api.Game{ID: "g1", Timeout: 500},
		Turn:  10,
		Board: api.Board{Width: 11, Height: 11, Snakes: snakes, Food: food},
		You:   snakes[0],
	}
}

// tradeOffRequest is a board where the leading moves are close in score but
// genuinely different: "left" is a step from food while "up" and "right" are
// three steps away. That is the shape that earns an inference call, as opposed
// to a symmetric board where the options are interchangeable.
func tradeOffRequest() api.GameRequest {
	return request([]api.Battlesnake{
		snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3)),
	}, []api.Coord{coord(3, 5)})
}

// splitBoardRequest puts our body across row y=3 from the left edge, so "up"
// opens into 77 squares and "down" into 33, with our head the only square
// joining them. The scorer prefers up.
func splitBoardRequest() api.GameRequest {
	body := []api.Coord{coord(0, 3)}
	for x := 1; x <= 10; x++ {
		body = append(body, coord(x, 3))
	}
	me := api.Battlesnake{ID: "me", Health: 100, Body: body, Head: body[0], Length: len(body)}
	return request([]api.Battlesnake{me}, nil)
}

// withDeadline mirrors what the handler does with game.timeout.
func withDeadline(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestDecideAlwaysReturnsALegalMove(t *testing.T) {
	legal := map[string]bool{"up": true, "down": true, "left": true, "right": true}

	tests := []struct {
		name  string
		req   api.GameRequest
		asker jev.Asker
	}{
		{
			name: "boxed into a corner with no safe move",
			req: request([]api.Battlesnake{
				snake("me", 90, coord(0, 0), coord(0, 1), coord(1, 1), coord(1, 0)),
			}, nil),
		},
		{
			name: "open board",
			req: request([]api.Battlesnake{
				snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3)),
			}, []api.Coord{coord(5, 8)}),
		},
		{
			name: "inference disabled",
			req: request([]api.Battlesnake{
				snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3)),
			}, nil),
			asker: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDecider(tc.asker, DefaultConfig(), quietLogger())
			got := d.Decide(withDeadline(t, 500*time.Millisecond), tc.req, newGameState("g1"))
			if !legal[got.Move] {
				t.Fatalf("Decide() returned %q, which is not a legal move", got.Move)
			}
		})
	}
}

func TestDecideNeverWalksIntoItself(t *testing.T) {
	// Head at (0,0) with the body running right: only "up" survives.
	req := request([]api.Battlesnake{
		snake("me", 90, coord(0, 0), coord(1, 0), coord(2, 0)),
	}, nil)
	d := NewDecider(nil, DefaultConfig(), quietLogger())
	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))
	if got.Move != "up" {
		t.Fatalf("Decide() = %q, want \"up\" (the only survivable move)", got.Move)
	}
	if got.Reason != ReasonOnlyMove {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonOnlyMove)
	}
}

// TestDecideSkipsInferenceWhenItCannotHelp is the token budget in test form:
// these are the situations that must never cost a tie-break call.
func TestDecideSkipsInferenceWhenItCannotHelp(t *testing.T) {
	tests := []struct {
		name       string
		req        api.GameRequest
		ctxBudget  time.Duration
		tieBreak   bool
		wantReason Reason
	}{
		{
			name: "only one safe move",
			req: request([]api.Battlesnake{
				snake("me", 90, coord(0, 0), coord(1, 0), coord(2, 0)),
			}, nil),
			ctxBudget:  500 * time.Millisecond,
			tieBreak:   true,
			wantReason: ReasonOnlyMove,
		},
		{
			name: "one move is clearly better",
			req: request([]api.Battlesnake{
				// On the left edge: "up" and "right" are the only options, and
				// "right" walks into a longer rival's head.
				snake("me", 90, coord(0, 5), coord(0, 4), coord(0, 3)),
				snake("rival", 90, coord(2, 5), coord(3, 5), coord(4, 5), coord(5, 5)),
			}, nil),
			ctxBudget:  500 * time.Millisecond,
			tieBreak:   true,
			wantReason: ReasonClearWinner,
		},
		{
			name:       "budget too small for a call",
			req:        tradeOffRequest(),
			ctxBudget:  50 * time.Millisecond,
			tieBreak:   true,
			wantReason: ReasonNoBudget,
		},
		{
			name:       "tie-break switched off",
			req:        tradeOffRequest(),
			ctxBudget:  500 * time.Millisecond,
			tieBreak:   false,
			wantReason: ReasonTieBreakDisabled,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFake("up")
			cfg := DefaultConfig()
			cfg.TieBreak = tc.tieBreak
			d := NewDecider(fake, cfg, quietLogger())

			got := d.Decide(withDeadline(t, tc.ctxBudget), tc.req, newGameState("g1"))
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if n := fake.count("move"); n != 0 {
				t.Errorf("made %d tie-break calls, want 0", n)
			}
		})
	}
}

func TestDecideNoDeadlineMeansNoCall(t *testing.T) {
	fake := newFake("up")
	d := NewDecider(fake, DefaultConfig(), quietLogger())

	got := d.Decide(context.Background(), tradeOffRequest(), newGameState("g1"))
	if got.Reason != ReasonNoBudget {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonNoBudget)
	}
	if n := fake.count("move"); n != 0 {
		t.Errorf("made %d tie-break calls without a deadline, want 0", n)
	}
}

func TestDecideUsesInferenceOnAGenuineTradeOff(t *testing.T) {
	req := tradeOffRequest()

	// The deterministic pick here is "left", the food. Answering "right"
	// proves the judgment actually decides the move rather than rubber-stamping.
	fake := newFake("right")
	fake.latency = 200 * time.Millisecond
	gs := newGameState("g1")
	d := NewDecider(fake, DefaultConfig(), quietLogger())

	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, gs)
	if got.Reason != ReasonJev {
		t.Fatalf("Reason = %q, want %q", got.Reason, ReasonJev)
	}
	if got.Move != "right" {
		t.Errorf("Move = %q, want %q: inference did not decide the move", got.Move, "right")
	}
	if got.Candidates[0].Dir.String() != "left" {
		t.Errorf("deterministic top = %q, want \"left\"", got.Candidates[0].Dir)
	}
	if got.Tokens != 400 {
		t.Errorf("Tokens = %d, want 400", got.Tokens)
	}
	if calls, tokens, _, _ := gs.Stats(); calls == 0 || tokens == 0 {
		t.Errorf("Stats() = (%d, %d), want both non-zero", calls, tokens)
	}
}

func TestDecideFallsBackWhenInferenceMisbehaves(t *testing.T) {
	req := tradeOffRequest()
	legal := map[string]bool{"up": true, "down": true, "left": true, "right": true}

	tests := []struct {
		name    string
		prepare func(*fakeAsker)
	}{
		{
			name:    "call returns an error",
			prepare: func(f *fakeAsker) { f.err = errors.New("boom") },
		},
		{
			name:    "answer is not one of the safe moves",
			prepare: func(f *fakeAsker) { f.answer = "sideways" },
		},
		{
			name: "call exceeds the turn deadline",
			prepare: func(f *fakeAsker) {
				f.block = make(chan struct{}) // never closed
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFake("right")
			tc.prepare(fake)
			gs := newGameState("g1")
			d := NewDecider(fake, DefaultConfig(), quietLogger())

			start := time.Now()
			got := d.Decide(withDeadline(t, 500*time.Millisecond), req, gs)
			elapsed := time.Since(start)

			if got.Reason != ReasonJevFailed {
				t.Errorf("Reason = %q, want %q", got.Reason, ReasonJevFailed)
			}
			if !legal[got.Move] {
				t.Errorf("Move = %q, which is not legal", got.Move)
			}
			if elapsed > 500*time.Millisecond {
				t.Errorf("took %v, which is past the turn deadline", elapsed)
			}
			if _, _, fallbacks, _ := gs.Stats(); fallbacks != 1 {
				t.Errorf("fallbacks = %d, want 1", fallbacks)
			}
		})
	}
}

func TestDecideAvoidsLosingHeadToHead(t *testing.T) {
	// We are at (5,5) with our body below. A longer snake's head is at (7,5),
	// so moving right risks a head-to-head we would lose.
	me := snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3))
	rival := snake("rival", 90, coord(7, 5), coord(8, 5), coord(9, 5), coord(9, 6), coord(9, 7))
	req := request([]api.Battlesnake{me, rival}, nil)

	d := NewDecider(nil, DefaultConfig(), quietLogger())
	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))
	if got.Move == "right" {
		t.Error("moved into a head-to-head against a longer snake")
	}
}

func TestLowHealthPrefersFood(t *testing.T) {
	low := weights(ModeHunt, 10, DefaultConfig().LowHealth)
	high := weights(ModeHunt, 90, DefaultConfig().LowHealth)
	if low.food <= high.food {
		t.Errorf("low-health food weight %.2f should exceed healthy weight %.2f", low.food, high.food)
	}
	if low.hunt != 0 {
		t.Errorf("hunting weight = %.2f at low health, want 0", low.hunt)
	}
}

func TestTieBreakRequestSendsOnlySafeMovesAndNoIdentifiers(t *testing.T) {
	req := request([]api.Battlesnake{
		snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3)),
	}, []api.Coord{coord(5, 8)})
	cands := []Candidate{
		{Dir: game.Left, Space: 40, FoodDist: 5, HasFood: true, H2H: game.H2HNone},
		{Dir: game.Right, Space: 18, FoodDist: 7, HasFood: true, H2H: game.H2HLose},
	}

	got := tieBreakRequest(req, ModeSurvive, cands, false, false, false)

	criteria, ok := got.Questions["move"].Criteria.(map[string]string)
	if !ok {
		t.Fatalf("criteria type = %T, want map[string]string", got.Questions["move"].Criteria)
	}
	if len(criteria) != 2 {
		t.Errorf("offered %d options, want only the 2 safe ones", len(criteria))
	}
	if _, offered := criteria["up"]; offered {
		t.Error("offered a move that was not in the safe set")
	}

	state, ok := got.State.(map[string]any)
	if !ok {
		t.Fatalf("state type = %T, want map[string]any", got.State)
	}
	for _, banned := range []string{"id", "you", "board", "snakes", "customizations", "shout", "latency"} {
		if _, present := state[banned]; present {
			t.Errorf("state carries %q, which wastes tokens and helps nothing", banned)
		}
	}
}

func TestDescribeFlagsAPocketSmallerThanUs(t *testing.T) {
	got := describe(Candidate{Dir: game.Up, Space: 3, H2H: game.H2HNone}, 9)
	if !strings.Contains(got, "smaller than my body") {
		t.Errorf("describe() = %q, want it to flag the pocket", got)
	}
}

// TestScorerPrefersKeepingTheTailReachable covers the heuristic that most
// determines how long a long snake survives.
func TestScorerPrefersKeepingTheTailReachable(t *testing.T) {
	// Our body runs along the bottom. Moving up keeps the tail reachable;
	// moving into the sealed left column does not.
	me := snake("me", 90,
		coord(0, 5), coord(0, 6), coord(0, 7), coord(0, 8), coord(0, 9), coord(0, 10))
	wall := snake("wall", 100,
		coord(1, 0), coord(1, 1), coord(1, 2), coord(1, 3), coord(1, 4),
		coord(1, 5), coord(1, 6), coord(1, 7), coord(1, 8), coord(1, 9), coord(1, 10))
	req := request([]api.Battlesnake{me, wall}, nil)

	d := NewDecider(nil, DefaultConfig(), quietLogger())
	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))

	for _, c := range got.Candidates {
		if c.Dir.String() == got.Move && !c.TailSafe {
			// Only acceptable if no option kept the tail reachable at all.
			for _, other := range got.Candidates {
				if other.TailSafe {
					t.Errorf("chose %q with the tail cut off while %q kept it reachable",
						got.Move, other.Dir)
				}
			}
		}
	}
}

// TestScorerDiscriminatesBetweenRoomyOptions guards a saturation bug: a space
// term capped at 1 scored a 77-square region the same as a 33-square one, which
// made the scorer flat exactly where it mattered.
func TestScorerDiscriminatesBetweenRoomyOptions(t *testing.T) {
	// Our body spans row y=3 from the left edge, splitting the board into a
	// 77-square region above and a 33-square one below. Our head is the only
	// square joining them.
	body := []api.Coord{coord(0, 3)}
	for x := 1; x <= 10; x++ {
		body = append(body, coord(x, 3))
	}
	me := api.Battlesnake{ID: "me", Health: 100, Body: body, Head: body[0], Length: len(body)}
	req := request([]api.Battlesnake{me}, nil)

	d := NewDecider(nil, DefaultConfig(), quietLogger())
	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))

	byDir := map[string]Candidate{}
	for _, c := range got.Candidates {
		byDir[c.Dir.String()] = c
	}
	up, down := byDir["up"], byDir["down"]
	if up.Space != 77 || down.Space != 33 {
		t.Fatalf("regions = up %d / down %d, want 77 / 33", up.Space, down.Space)
	}
	if up.Score <= down.Score {
		t.Errorf("scores = up %.4f / down %.4f, want the larger region to score higher",
			up.Score, down.Score)
	}
	if got.Move != "up" {
		t.Errorf("Move = %q, want \"up\" into the larger region", got.Move)
	}
}

func TestColdPoolTriggersABackgroundWarm(t *testing.T) {
	fake := newFake("left")
	d := NewDecider(fake, DefaultConfig(), quietLogger())
	// Report cold so the tie-break is skipped, and record warm-up attempts.
	d.cold = func() bool { return true }
	warmed := make(chan struct{}, 1)
	d.warm = func(context.Context) error {
		select {
		case warmed <- struct{}{}:
		default:
		}
		return nil
	}

	got := d.Decide(withDeadline(t, 500*time.Millisecond), tradeOffRequest(), newGameState("g1"))
	if got.Reason != ReasonColdPool {
		t.Fatalf("Reason = %q, want %q", got.Reason, ReasonColdPool)
	}
	select {
	case <-warmed:
	case <-time.After(2 * time.Second):
		t.Error("a cold pool did not trigger a background warm-up")
	}
}

// TestInterchangeableOptionsCostNothing is the token budget's main lever: a
// close score between options that are identical in every measured way must
// not become an inference call.
func TestInterchangeableOptionsCostNothing(t *testing.T) {
	// A symmetric open board: up, left and right differ in nothing at all.
	req := request([]api.Battlesnake{
		snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3)),
	}, nil)

	fake := newFake("left")
	d := NewDecider(fake, DefaultConfig(), quietLogger())
	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))

	if got.Reason != ReasonInterchangeable {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonInterchangeable)
	}
	if n := fake.count("move"); n != 0 {
		t.Errorf("spent %d calls choosing between identical options, want 0", n)
	}
}

func TestFeaturesIgnoreTrivialDifferences(t *testing.T) {
	base := Candidate{Space: 40, FoodDist: 6, HasFood: true, TailSafe: true, H2H: game.H2HNone}

	nudged := base
	nudged.Space = 41
	nudged.FoodDist = 7
	if features(base) != features(nudged) {
		t.Error("a one-square difference should not count as a trade-off")
	}

	realDifference := base
	realDifference.TailSafe = false
	if features(base) == features(realDifference) {
		t.Error("losing tail access is a trade-off and must be visible")
	}
}

// TestAlwaysAskBypassesTheCheapPaths covers the measurement mode: when it is on,
// a real choice always reaches inference even if the deterministic scores were
// decisive or the options were interchangeable.
func TestAlwaysAskBypassesTheCheapPaths(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AlwaysAsk = true

	tests := []struct {
		name string
		req  api.GameRequest
	}{
		{
			name: "options that are interchangeable",
			req: request([]api.Battlesnake{
				snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3)),
			}, nil),
		},
		{
			name: "a decisive deterministic winner",
			req: request([]api.Battlesnake{
				snake("me", 90, coord(0, 5), coord(0, 4), coord(0, 3)),
				snake("rival", 90, coord(2, 5), coord(3, 5), coord(4, 5), coord(5, 5)),
			}, nil),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFake("up")
			d := NewDecider(fake, cfg, quietLogger())
			got := d.Decide(withDeadline(t, 500*time.Millisecond), tc.req, newGameState("g1"))
			if got.Reason != ReasonJev {
				t.Errorf("Reason = %q, want %q", got.Reason, ReasonJev)
			}
			if n := fake.count("move"); n != 1 {
				t.Errorf("inference calls = %d, want 1", n)
			}
		})
	}
}

// TestAlwaysAskStillRefusesUnsafeAnswers: measurement mode must not weaken the
// safety guarantee.
func TestAlwaysAskStillRefusesUnsafeAnswers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AlwaysAsk = true
	fake := newFake("right") // walks into our own neck
	d := NewDecider(fake, cfg, quietLogger())

	req := request([]api.Battlesnake{
		snake("me", 90, coord(0, 0), coord(1, 0), coord(2, 0)),
	}, nil)

	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))
	if got.Move != "up" {
		t.Errorf("Move = %q, want \"up\": an unsafe answer must never be played", got.Move)
	}
}

// TestOverridesAreCounted: knowing how often the model disagrees with the code
// is the only way to tell whether it is contributing or just agreeing.
func TestOverridesAreCounted(t *testing.T) {
	gs := newGameState("g1")
	fake := newFake("right") // the deterministic pick here is "left"
	d := NewDecider(fake, DefaultConfig(), quietLogger())

	got := d.Decide(withDeadline(t, 500*time.Millisecond), tradeOffRequest(), gs)
	if got.Reason != ReasonJev {
		t.Fatalf("Reason = %q, want %q", got.Reason, ReasonJev)
	}
	if _, _, _, overrides := gs.Stats(); overrides != 1 {
		t.Errorf("overrides = %d, want 1", overrides)
	}

	agreeing := newGameState("g2")
	agree := newFake("left") // matches the deterministic pick
	da := NewDecider(agree, DefaultConfig(), quietLogger())
	da.Decide(withDeadline(t, 500*time.Millisecond), tradeOffRequest(), agreeing)
	if _, _, _, overrides := agreeing.Stats(); overrides != 0 {
		t.Errorf("overrides = %d when the model agreed, want 0", overrides)
	}
}

// TestATieIsPenalisedLikeADeath guards the weighting that was letting snakes
// trade themselves away in the opening, when every snake is the same length and
// so every head-to-head eliminates both.
func TestATieIsPenalisedLikeADeath(t *testing.T) {
	if losePenalty-tiePenalty > 0.10 {
		t.Errorf("tie penalty %.2f is far below the loss penalty %.2f, but both are fatal",
			tiePenalty, losePenalty)
	}

	// Our head at (5,5); an equal-length rival two squares to the right, so
	// moving right is a mutual kill. Every other safe move must beat it.
	me := snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3))
	rival := snake("rival", 90, coord(7, 5), coord(8, 5), coord(9, 5))
	req := request([]api.Battlesnake{me, rival}, []api.Coord{coord(7, 6)})

	d := NewDecider(nil, DefaultConfig(), quietLogger())
	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))

	if got.Move == "right" {
		t.Error("walked into an equal-length head-to-head, which eliminates us too")
	}
	for _, c := range got.Candidates {
		if c.H2H == game.H2HTie && c.Dir.String() == got.Move {
			t.Errorf("chose %q despite a mutual-kill risk", got.Move)
		}
	}
}

// TestStandoffPrefersTheLeastContestedSquare reproduces the opening standoff
// that was killing two snakes at once: four equal-length snakes around the
// centre, where every move risks a mutual kill. The scorer cannot avoid the
// risk, but it can take the one that fewest rivals can contest.
func TestStandoffPrefersTheLeastContestedSquare(t *testing.T) {
	me := snake("me", 95, coord(4, 5), coord(3, 5), coord(2, 5), coord(1, 5))
	up := snake("u", 95, coord(5, 6), coord(5, 7), coord(5, 8), coord(5, 9))
	down := snake("d", 95, coord(5, 4), coord(5, 3), coord(5, 2), coord(5, 1))
	right := snake("r", 95, coord(6, 5), coord(7, 5), coord(8, 5), coord(9, 5))
	req := request([]api.Battlesnake{me, up, down, right}, []api.Coord{coord(5, 5)})

	d := NewDecider(nil, DefaultConfig(), quietLogger())
	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))

	byDir := map[string]Candidate{}
	for _, c := range got.Candidates {
		byDir[c.Dir.String()] = c
	}
	// Moving right onto the food is contested by three rivals; up and down by
	// one each. The food must not buy the crowded square.
	if got.Move == "right" {
		t.Errorf("took the square three rivals contest (%d contesters) for the food",
			byDir["right"].Contesters)
	}
	if c, ok := byDir[got.Move]; ok && c.Contesters > 1 {
		t.Errorf("chose %q with %d contesters when a less contested move existed",
			got.Move, c.Contesters)
	}
}
