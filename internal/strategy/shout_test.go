package strategy

import (
	"strings"
	"testing"

	"github.com/n0nuser/battlesnake-jev/internal/game"
)

func TestShoutShowsTheRoomReading(t *testing.T) {
	d := Decision{
		Move: "up", Reason: ReasonJevAlarm, Mode: ModeSurvive,
		Advice:     Advice{Present: true, Room: 1},
		Candidates: []Candidate{{Dir: game.Up, Space: 12, TailSafe: true}},
	}
	got := d.Shout()
	if !strings.Contains(got, "room 75% gone") {
		t.Errorf("Shout() = %q, want it to report the room reading", got)
	}
}

func TestShoutNamesWhoDecided(t *testing.T) {
	tests := []struct {
		name     string
		decision Decision
		want     []string
	}{
		{
			name: "the model decided",
			decision: Decision{
				Move: "left", Reason: ReasonJev, Mode: ModeHunt, Confidence: 0.87,
				Candidates: []Candidate{{Dir: game.Left, Space: 42, TailSafe: true}},
			},
			want: []string{"jev left", "87%", "hunt", "42 free"},
		},
		{
			name: "the model was asked but missed the deadline",
			decision: Decision{
				Move: "up", Reason: ReasonJevFailed, Mode: ModeSurvive,
				Candidates: []Candidate{{Dir: game.Up, Space: 30, TailSafe: true}},
			},
			want: []string{"timed out", "up"},
		},
		{
			name: "the code decided on its own",
			decision: Decision{
				Move: "down", Reason: ReasonClearWinner, Mode: ModeSurvive,
				Candidates: []Candidate{{Dir: game.Down, Space: 55, TailSafe: true}},
			},
			want: []string{"code down", "survive", "55 free"},
		},
		{
			name: "nothing was safe",
			decision: Decision{
				Move: "right", Reason: ReasonTrapped, Mode: ModeSurvive,
			},
			want: []string{"boxed in", "right"},
		},
		{
			name: "a mutual kill is on the table",
			decision: Decision{
				Move: "up", Reason: ReasonJev, Mode: ModeSurvive, Confidence: 0.5,
				Candidates: []Candidate{
					{Dir: game.Up, Space: 20, TailSafe: false, H2H: game.H2HTie, Contesters: 2},
				},
			},
			want: []string{"tail cut off", "2 rival(s) could trade"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.decision.Shout()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("Shout() = %q, want it to contain %q", got, want)
				}
			}
			if len(got) > maxShout {
				t.Errorf("Shout() is %d bytes, over the %d limit", len(got), maxShout)
			}
		})
	}
}

func TestShoutStaysWithinTheLimit(t *testing.T) {
	d := Decision{
		Move: "left", Reason: ReasonJev, Confidence: 0.9,
		Mode:       Mode(strings.Repeat("verbose", 60)),
		Candidates: []Candidate{{Dir: game.Left, Space: 100, H2H: game.H2HTie, Contesters: 3}},
	}
	if got := d.Shout(); len(got) != maxShout {
		t.Errorf("Shout() is %d bytes, want it truncated to exactly %d", len(got), maxShout)
	}
}
