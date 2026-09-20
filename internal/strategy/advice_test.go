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

	without := tieBreakRequest(req, ModeSurvive, cands, true, false)
	if len(without.Questions) != 1 {
		t.Errorf("asked %d questions with advisories off, want 1", len(without.Questions))
	}

	with := tieBreakRequest(req, ModeSurvive, cands, true, true)
	if len(with.Questions) != 3 {
		t.Fatalf("asked %d questions with advisories on, want 3", len(with.Questions))
	}
	for _, id := range []string{"sealed", "hunted"} {
		q, ok := with.Questions[id]
		if !ok {
			t.Fatalf("question %q was not asked", id)
		}
		if q.Type != jev.TypeNoul {
			t.Errorf("question %q has type %q, want %q", id, q.Type, jev.TypeNoul)
		}
		if _, ok := q.Criteria.(map[string]string); !ok {
			t.Errorf("question %q has no true/false criteria", id)
		}
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
		{"not asked", Advice{Sealed: 0.9, Hunted: 0.9}, false},
		{"both quiet", Advice{Present: true, Sealed: 0.1, Hunted: 0.2}, false},
		{"space closing", Advice{Present: true, Sealed: 0.8}, true},
		{"being cut off", Advice{Present: true, Hunted: 0.7}, true},
		{"right on the threshold", Advice{Present: true, Sealed: 0.65}, true},
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
	if !got.Present {
		t.Error("Present = false although the advisories were asked for")
	}
	if got.Sealed != 0 || got.Hunted != 0 {
		t.Errorf("missing answers produced a warning: %+v", got)
	}
	if got.Alarmed(0.65) {
		t.Error("a partial reply must never raise an alarm")
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

	fake := newFake("down") // the model proposes the small region
	fake.nouls = map[string]float64{"sealed": 0.9}
	gs := newGameState("g1")
	d := NewDecider(fake, advisorConfig(), quietLogger())

	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, gs)

	if got.Reason != ReasonJevAlarm {
		t.Fatalf("Reason = %q, want %q", got.Reason, ReasonJevAlarm)
	}
	if got.Move != "up" {
		t.Errorf("Move = %q, want \"up\": the alarm should send us to open space", got.Move)
	}
	if got.Advice.Sealed != 0.9 {
		t.Errorf("Advice.Sealed = %.2f, want 0.90", got.Advice.Sealed)
	}
}

func TestQuietAdvisoriesLeaveTheMoveAlone(t *testing.T) {
	fake := newFake("right")
	fake.nouls = map[string]float64{"sealed": 0.05, "hunted": 0.10}
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
	fake.nouls = map[string]float64{"sealed": 0.99, "hunted": 0.99}
	d := NewDecider(fake, advisorConfig(), quietLogger())

	got := d.Decide(withDeadline(t, 500*time.Millisecond), req, newGameState("g1"))
	if got.Move != "up" {
		t.Errorf("Move = %q, want \"up\": the only safe move", got.Move)
	}
}
