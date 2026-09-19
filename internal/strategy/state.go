// Package strategy turns a board into a move.
//
// Safety and tactics are deterministic Go code. Inference supplies only the
// strategic judgment code cannot express, and it is never allowed to delay or
// replace a legal move: the deterministic answer is computed first and held, so
// every path out of Decide already has a move in hand.
package strategy

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Mode is the standing posture inference picks for us. It weights the
// deterministic scoring; it never chooses a move by itself.
type Mode string

// The postures the model may choose between.
const (
	// ModeSurvive prioritises reachable space above everything else.
	ModeSurvive Mode = "survive"
	// ModeEat prioritises closing on food.
	ModeEat Mode = "eat"
	// ModeHunt prioritises head-to-head collisions we would win.
	ModeHunt Mode = "hunt"
	// ModeTrap prioritises cutting opponents off from open space.
	ModeTrap Mode = "trap"
)

// validModes is the set accepted back from the model; anything else is ignored.
var validModes = map[Mode]bool{
	ModeSurvive: true,
	ModeEat:     true,
	ModeHunt:    true,
	ModeTrap:    true,
}

// seedLatency is the measured p90 of a warm inference call. The latency
// estimate starts here rather than at zero so the very first tie-break does not
// optimistically fire a call it cannot afford.
const seedLatency = 330 * time.Millisecond

// GameState is the per-game scratch space. It is created on /start and dropped
// on /end. It must never be package-level: several games run concurrently and a
// shared posture would leak one game's strategy into another's moves.
type GameState struct {
	ID string

	mode atomic.Value // Mode

	// latencyNanos is an EWMA of observed inference latency.
	latencyNanos atomic.Int64

	// cancel stops any in-flight background posture refresh.
	cancel context.CancelFunc

	// mu guards the shift fingerprint below.
	mu          sync.Mutex
	fingerprint situation
	refreshing  bool
	lastRefresh int

	calls       atomic.Int64
	inputTokens atomic.Int64
	fallbacks   atomic.Int64
}

// situation is the coarse fingerprint used to decide whether the posture is
// still worth re-asking. Re-asking every turn would spend tokens on a judgment
// that has not changed.
type situation struct {
	healthBucket   int
	opponentsAlive int
	longest        bool
	initialised    bool
}

func newGameState(id string) *GameState {
	gs := &GameState{ID: id, lastRefresh: -minRefreshGap}
	gs.mode.Store(ModeSurvive)
	gs.latencyNanos.Store(int64(seedLatency))
	return gs
}

// Mode returns the current posture, defaulting to survival.
func (g *GameState) Mode() Mode {
	if m, ok := g.mode.Load().(Mode); ok {
		return m
	}
	return ModeSurvive
}

// EstimatedLatency returns the current estimate of an inference round trip.
func (g *GameState) EstimatedLatency() time.Duration {
	return time.Duration(g.latencyNanos.Load())
}

// observeLatency folds a new sample into the EWMA.
func (g *GameState) observeLatency(d time.Duration) {
	const alpha = 4 // weight of 1/alpha on the new sample
	old := g.latencyNanos.Load()
	g.latencyNanos.Store(old + (int64(d)-old)/alpha)
}

// Stats reports what this game spent, which is the headline number for a
// project whose goal is the fewest input tokens for the best play.
func (g *GameState) Stats() (calls, inputTokens, fallbacks int64) {
	return g.calls.Load(), g.inputTokens.Load(), g.fallbacks.Load()
}

// Store holds live games. A game that never receives /end would otherwise leak,
// so entries also carry a creation time and are swept.
type Store struct {
	mu    sync.Mutex
	games map[string]*storeEntry
	ttl   time.Duration
	now   func() time.Time
}

type storeEntry struct {
	state   *GameState
	created time.Time
}

// NewStore returns an empty store. Games untouched for ttl are swept, so a game
// that never sends /end cannot leak.
func NewStore(ttl time.Duration) *Store {
	return &Store{games: make(map[string]*storeEntry), ttl: ttl, now: time.Now}
}

// Start creates state for a game, replacing any stale entry with the same id.
func (s *Store) Start(id string) *GameState {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	if e, ok := s.games[id]; ok {
		return e.state
	}
	gs := newGameState(id)
	s.games[id] = &storeEntry{state: gs, created: s.now()}
	return gs
}

// Get returns the state for a game, creating it if /start was missed.
func (s *Store) Get(id string) *GameState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.games[id]; ok {
		return e.state
	}
	gs := newGameState(id)
	s.games[id] = &storeEntry{state: gs, created: s.now()}
	return gs
}

// End drops a game's state and stops its background work.
func (s *Store) End(id string) *GameState {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.games[id]
	if !ok {
		return nil
	}
	delete(s.games, id)
	if e.state.cancel != nil {
		e.state.cancel()
	}
	return e.state
}

// Len reports how many games are live, for tests and for logging.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.games)
}

func (s *Store) sweepLocked() {
	if s.ttl <= 0 {
		return
	}
	cutoff := s.now().Add(-s.ttl)
	for id, e := range s.games {
		if e.created.Before(cutoff) {
			if e.state.cancel != nil {
				e.state.cancel()
			}
			delete(s.games, id)
		}
	}
}
