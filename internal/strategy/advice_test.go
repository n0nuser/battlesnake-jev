package strategy

import (
	"testing"
	"time"

	"github.com/n0nuser/battlesnake-jev/internal/api"
	"github.com/n0nuser/battlesnake-jev/internal/game"
	"github.com/n0nuser/battlesnake-jev/internal/jev"
)

func advisorConfig() Config {
	cfg := DefaultConfig()
	cfg.Advisors = true
	return cfg
}

func TestAdvisoriesRideAlongWithTheMove(t *testing.T) {
	req := tradeOffRequest()
	cands := []Candidate{
		{Dir: game.Left, Space: 40, FoodDist: 5, HasFood: true, TailSafe: true},
		{Dir: game.Right, Space: 18, FoodDist: 7, HasFood: true, TailSafe: true},
	}

	without := tieBreakRequest(req, ModeSurvive, cands, true, false, false)
	if len(without.Questions) != 1 {
		t.Errorf("asked %d questions with advisories off, want 1", len(without.Questions))
	}

	with := tieBreakRequest(req, ModeSurvive, cands, true, true, false)
	if len(with.Questions) != 2 {
		t.Fatalf("asked %d questions with advisories on, want 2", len(with.Questions))
	}
	q, ok := with.Questions["room"]
	if !ok {
		t.Fatal("the room question was not asked")
	}
	if q.Type != jev.TypeScore {
		t.Errorf("room has type %q, want %q", q.Type, jev.TypeScore)
	}
	levels, ok := q.Criteria.([]string)
	if !ok || len(levels) < 2 {
		t.Errorf("room criteria = %v, want at least two ordered levels", q.Criteria)
	}
	// One request, so the board is sent once however many questions ride on it.
	if with.State == nil {
		t.Error("advisories were sent without any state to judge")
	}
}

func TestAlarmed(t *testing.T) {
	tests := []struct {
		name   string
		advice Advice
		want   bool
	}{
		{"not asked", Advice{Room: 0}, false},
		{"wide open", Advice{Present: true, Room: 4}, false},
		{"comfortable", Advice{Present: true, Room: 3}, false},
		{"tight", Advice{Present: true, Room: 1}, true},
		{"boxed in", Advice{Present: true, Room: 0}, true},
		{"right on the threshold", Advice{Present: true, Room: 1.4}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.advice.Alarmed(0.65); got != tc.want {
				t.Errorf("Alarmed() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReadAdviceTreatsAMissingAnswerAsQuiet(t *testing.T) {
	resp := &jev.Response{Answers: map[string]jev.Answer{
		"move": {Type: jev.TypeChoice, Choice: "left"},
	}}
	got := readAdvice(resp, true)
	if got.Present {
		t.Error("Present = true although no room answer came back")
	}
	if got.Alarmed(0.65) {
		t.Error("a partial reply must never raise an alarm")
	}
}

func TestConfinementMapsTheScale(t *testing.T) {
	tests := []struct {
		name   string
		advice Advice
		want   float64
	}{
		{"not asked", Advice{Room: 0}, 0},
		{"boxed in", Advice{Present: true, Room: 0}, 1},
		{"wide open", Advice{Present: true, Room: 4}, 0},
		{"halfway", Advice{Present: true, Room: 2}, 0.5},
		{"out of range is clamped", Advice{Present: true, Room: 9}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.advice.Confinement(); got != tc.want {
				t.Errorf("Confinement() = %.2f, want %.2f", got, tc.want)
			}
		})
	}
}

// TestAlarmOverridesTheModelsOwnMove: a warning outranks the move, including
// the move the same reply proposed.
func TestAlarmOverridesTheModelsOwnMove(t *testing.T) {
	// Our body runs along row y=3 from the left edge, so "up" opens into 77
	// squares and "down" into 33. Food sits in the small region to tempt us.
	body := []api.Coord{coord(0, 3)}
	for x := 1; x <= 10; x++ {
		body = append(body, coord(x, 3))
	}
	me := api.Battlesnake{ID: "me", Health: 100, Body: body, Head: body[0], Length: len(body)}
	req := request([]api.Battlesnake{me}, []api.Coord{coord(0, 0)})

	fake := newFake("down")                     // the model proposes the small region
	fake.scores = map[string]float64{"room": 0} // boxed in
	gs := newGameState("g1")
	d := NewDecider(fake, advisorConfig(), quietLogger())

	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, gs)

	if got.Reason != ReasonJevAlarm {
		t.Fatalf("Reason = %q, want %q", got.Reason, ReasonJevAlarm)
	}
	if got.Move != "up" {
		t.Errorf("Move = %q, want \"up\": the alarm should send us to open space", got.Move)
	}
	if got.Advice.Confinement() != 1 {
		t.Errorf("Confinement() = %.2f, want 1.00", got.Advice.Confinement())
	}
}

func TestQuietAdvisoriesLeaveTheMoveAlone(t *testing.T) {
	fake := newFake("right")
	fake.scores = map[string]float64{"room": 4} // wide open
	d := NewDecider(fake, advisorConfig(), quietLogger())

	got := d.Decide(withDeadline(t, 500*time.Millisecond), tradeOffRequest(), newGameState("g1"))

	if got.Reason != ReasonJev {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonJev)
	}
	if got.Move != "right" {
		t.Errorf("Move = %q, want the model's own choice %q", got.Move, "right")
	}
	if !got.Advice.Present {
		t.Error("advisories were asked for but not recorded on the decision")
	}
}

// TestAlarmStillReturnsASafeMove: the override must never leave the safe set.
func TestAlarmStillReturnsASafeMove(t *testing.T) {
	req := request([]api.Battlesnake{
		snake("me", 90, coord(0, 0), coord(1, 0), coord(2, 0)),
	}, nil)

	fake := newFake("right") // into our own neck
	fake.scores = map[string]float64{"room": 0}
	d := NewDecider(fake, advisorConfig(), quietLogger())

	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))
	if got.Move != "up" {
		t.Errorf("Move = %q, want \"up\": the only safe move", got.Move)
	}
}
