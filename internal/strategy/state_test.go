package strategy

import (
	"testing"
	"time"

	"github.com/n0nuser/battlesnake-jev/internal/api"
)

func TestStoreLifecycle(t *testing.T) {
	s := NewStore(time.Hour)

	gs := s.Start("g1")
	if gs.Mode() != ModeSurvive {
		t.Errorf("new game mode = %q, want %q", gs.Mode(), ModeSurvive)
	}
	if gs.EstimatedLatency() != seedLatency {
		t.Errorf("seed latency = %v, want %v", gs.EstimatedLatency(), seedLatency)
	}
	if s.Start("g1") != gs {
		t.Error("Start() on a live game returned different state")
	}
	if s.Get("g1") != gs {
		t.Error("Get() returned different state from Start()")
	}
	if s.Len() != 1 {
		t.Errorf("Len() = %d, want 1", s.Len())
	}

	if s.End("g1") != gs {
		t.Error("End() returned different state")
	}
	if s.Len() != 0 {
		t.Errorf("Len() after End() = %d, want 0", s.Len())
	}
	if s.End("g1") != nil {
		t.Error("End() on an unknown game should return nil")
	}
}

// TestStoreIsolatesGames guards the bug that a package-level posture cache
// would cause: one game's strategy leaking into another's moves.
func TestStoreIsolatesGames(t *testing.T) {
	s := NewStore(time.Hour)
	a, b := s.Start("a"), s.Start("b")

	a.mode.Store(ModeHunt)
	if b.Mode() != ModeSurvive {
		t.Errorf("game b picked up game a's posture: %q", b.Mode())
	}

	a.inputTokens.Add(500)
	if _, tokens, _ := b.Stats(); tokens != 0 {
		t.Errorf("game b's token count = %d, want 0", tokens)
	}
}

// TestStoreSweepsAbandonedGames covers a game that never sends /end.
func TestStoreSweepsAbandonedGames(t *testing.T) {
	s := NewStore(time.Minute)
	now := time.Now()
	s.now = func() time.Time { return now }

	s.Start("old")
	now = now.Add(2 * time.Minute)
	s.Start("new")

	if s.Len() != 1 {
		t.Errorf("Len() = %d, want 1 after sweeping the abandoned game", s.Len())
	}
	if _, ok := s.games["old"]; ok {
		t.Error("abandoned game was not swept")
	}
}

func TestObserveLatencyMovesTowardTheSample(t *testing.T) {
	gs := newGameState("g1")
	for range 20 {
		gs.observeLatency(200 * time.Millisecond)
	}
	got := gs.EstimatedLatency()
	if got > 210*time.Millisecond || got < 190*time.Millisecond {
		t.Errorf("EstimatedLatency() = %v, want it to converge near 200ms", got)
	}
}

func TestFingerprintOnlyChangesOnMaterialShifts(t *testing.T) {
	base := request([]api.Battlesnake{
		snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3)),
		snake("rival", 90, coord(1, 1), coord(1, 2)),
	}, nil)

	oneLessHealth := base
	oneLessHealth.You.Health = 89
	oneLessHealth.Board.Snakes[0].Health = 89

	if fingerprint(base) != fingerprint(oneLessHealth) {
		t.Error("one point of health decay should not trigger a new posture call")
	}

	lowHealth := base
	lowHealth.You.Health = 20
	if fingerprint(base) == fingerprint(lowHealth) {
		t.Error("crossing a health band should trigger a new posture call")
	}

	alone := request([]api.Battlesnake{
		snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3)),
	}, nil)
	if fingerprint(base) == fingerprint(alone) {
		t.Error("a rival dying should trigger a new posture call")
	}
}

// TestPostureRefreshRunsOffTheCriticalPath checks that the background call
// updates the posture without the turn ever waiting for it.
func TestPostureRefreshRunsOffTheCriticalPath(t *testing.T) {
	fake := newFake(string(ModeHunt))
	gs := newGameState("g1")
	d := NewDecider(fake, DefaultConfig(), quietLogger())
	req := request([]api.Battlesnake{
		snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3)),
	}, nil)

	// The first turn sees the default posture; the refresh lands afterwards.
	if got := d.Decide(withDeadline(t, 500*time.Millisecond), req, gs).Mode; got != ModeSurvive {
		t.Errorf("first turn posture = %q, want %q", got, ModeSurvive)
	}

	waitFor(t, func() bool { return gs.Mode() == ModeHunt })

	// An unchanged situation must not spend another posture call.
	before := fake.count("strategy")
	d.Decide(withDeadline(t, 500*time.Millisecond), req, gs)
	waitFor(t, func() bool { return true })
	if after := fake.count("strategy"); after != before {
		t.Errorf("posture calls went from %d to %d on an unchanged situation", before, after)
	}
}

func TestPostureRefreshRejectsUnknownModes(t *testing.T) {
	fake := newFake("panic")
	gs := newGameState("g1")
	d := NewDecider(fake, DefaultConfig(), quietLogger())
	req := request([]api.Battlesnake{
		snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3)),
	}, nil)

	d.Decide(withDeadline(t, 500*time.Millisecond), req, gs)
	waitFor(t, func() bool { return fake.count("strategy") > 0 })

	if got := gs.Mode(); got != ModeSurvive {
		t.Errorf("posture = %q after an unknown answer, want it unchanged at %q", got, ModeSurvive)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met within 2s")
}

// TestPostureRefreshIsRateLimited covers the token leak that health
// oscillating across a band boundary used to cause.
func TestPostureRefreshIsRateLimited(t *testing.T) {
	fake := newFake(string(ModeSurvive))
	gs := newGameState("g1")
	d := NewDecider(fake, DefaultConfig(), quietLogger())

	// Bounce health across a band boundary on consecutive turns.
	for turn, health := range map[int]int{1: 74, 2: 76, 3: 74, 4: 76, 5: 74} {
		req := request([]api.Battlesnake{
			snake("me", health, coord(5, 5), coord(5, 4), coord(5, 3)),
		}, nil)
		req.Turn = turn
		d.Decide(withDeadline(t, 500*time.Millisecond), req, gs)
	}
	waitFor(t, func() bool { return fake.count("strategy") > 0 })

	if got := fake.count("strategy"); got > 1 {
		t.Errorf("posture calls = %d over five adjacent turns, want at most 1", got)
	}
}

// TestRivalEliminationBypassesTheRateLimit: a rival dying really does change
// how the game should be played, so it must not be rate limited away.
func TestRivalEliminationBypassesTheRateLimit(t *testing.T) {
	fake := newFake(string(ModeHunt))
	gs := newGameState("g1")
	d := NewDecider(fake, DefaultConfig(), quietLogger())

	withRivals := request([]api.Battlesnake{
		snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3)),
		snake("r1", 90, coord(1, 1), coord(1, 2)),
	}, nil)
	withRivals.Turn = 1
	d.Decide(withDeadline(t, 500*time.Millisecond), withRivals, gs)
	waitFor(t, func() bool { return fake.count("strategy") == 1 })

	alone := request([]api.Battlesnake{
		snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3)),
	}, nil)
	alone.Turn = 2 // well inside the rate-limit window
	d.Decide(withDeadline(t, 500*time.Millisecond), alone, gs)
	waitFor(t, func() bool { return fake.count("strategy") == 2 })
}
