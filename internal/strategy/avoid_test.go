package strategy

import (
	"testing"
	"time"

	"github.com/n0nuser/battlesnake-jev/internal/api"
	"github.com/n0nuser/battlesnake-jev/internal/game"
)

func avoidConfig() Config {
	cfg := DefaultConfig()
	cfg.Avoid = true
	// These tests are about what happens once the model has answered, so the
	// cheap paths that would return before asking are turned off.
	cfg.AlwaysAsk = true
	return cfg
}

func TestAvoidQuestionOffersOnlySafeMovesPlusNone(t *testing.T) {
	req := tradeOffRequest()
	cands := []Candidate{
		{Dir: game.Left, Space: 40, FoodDist: 5, HasFood: true, TailSafe: true},
		{Dir: game.Right, Space: 18, FoodDist: 7, HasFood: true, TailSafe: true},
	}

	got := tieBreakRequest(req, ModeSurvive, cands, true, false, true)
	q, ok := got.Questions["avoid"]
	if !ok {
		t.Fatal("the avoid question was not asked")
	}
	criteria, ok := q.Criteria.(map[string]string)
	if !ok {
		t.Fatalf("avoid criteria type = %T, want map[string]string", q.Criteria)
	}
	if len(criteria) != 3 {
		t.Errorf("offered %d options, want the 2 safe moves plus %q", len(criteria), avoidNothing)
	}
	if _, ok := criteria[avoidNothing]; !ok {
		t.Errorf("no %q option: the model would have to condemn a move whatever the board looks like", avoidNothing)
	}
	if _, offered := criteria["up"]; offered {
		t.Error("offered a move that was not in the safe set")
	}
}

func TestAvoidStrikesTheNamedMove(t *testing.T) {
	// Body along row y=3: "up" opens 77 squares, "down" 33, so the scorer
	// prefers up. The model is told up becomes a trap.
	req := splitBoardRequest()

	fake := newFake("up")
	fake.choices = map[string]string{"avoid": "up"}
	gs := newGameState("g1")
	d := NewDecider(fake, avoidConfig(), quietLogger())

	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, gs)

	if got.Reason != ReasonJevAvoid {
		t.Fatalf("Reason = %q, want %q", got.Reason, ReasonJevAvoid)
	}
	if got.Move == "up" {
		t.Error("played the move the model struck off")
	}
	if got.Avoided != "up" {
		t.Errorf("Avoided = %q, want %q", got.Avoided, "up")
	}
	if _, _, _, overrides := gs.Stats(); overrides != 1 {
		t.Errorf("overrides = %d, want 1: the warning changed the move", overrides)
	}
}

func TestAvoidNeverStrikesTheLastMove(t *testing.T) {
	// Head at (0,0) with the body running right: "up" is the only safe move.
	req := request([]api.Battlesnake{snake("me", 90, coord(0, 0), coord(1, 0), coord(2, 0))}, nil)

	fake := newFake("up")
	fake.choices = map[string]string{"avoid": "up"}
	d := NewDecider(fake, avoidConfig(), quietLogger())

	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))
	if got.Move != "up" {
		t.Errorf("Move = %q, want \"up\": striking the only safe move would be suicide", got.Move)
	}
}

func TestAvoidRespectsTheConfidenceFloor(t *testing.T) {
	req := splitBoardRequest()

	fake := newFake("up")
	fake.choices = map[string]string{"avoid": "up"}
	fake.conf = map[string]float64{"avoid": 0.2} // below the floor
	d := NewDecider(fake, avoidConfig(), quietLogger())

	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))
	if got.Reason == ReasonJevAvoid {
		t.Error("acted on a warning the model was not confident about")
	}
}

func TestAvoidNoneLeavesTheMoveAlone(t *testing.T) {
	req := splitBoardRequest()

	fake := newFake("down")
	fake.choices = map[string]string{"avoid": avoidNothing}
	d := NewDecider(fake, avoidConfig(), quietLogger())

	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))
	if got.Reason == ReasonJevAvoid {
		t.Errorf("Reason = %q although the model declined to warn", got.Reason)
	}
}

func TestAvoidAnswerOutsideTheSafeSetIsIgnored(t *testing.T) {
	req := splitBoardRequest()

	fake := newFake("up")
	fake.choices = map[string]string{"avoid": "sideways"}
	d := NewDecider(fake, avoidConfig(), quietLogger())

	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))
	if got.Reason == ReasonJevAvoid {
		t.Error("acted on a warning naming a move that was never offered")
	}
}
