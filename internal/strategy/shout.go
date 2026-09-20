package strategy

import (
	"fmt"
	"strings"

	"github.com/n0nuser/battlesnake-jev/internal/game"
)

// maxShout is the limit the Battlesnake API puts on a shout.
const maxShout = 256

// Shout renders the decision as the short message the game board shows above
// the snake, so a spectator can watch the reasoning rather than infer it.
//
// It names who decided the move and why: the model when it was consulted, and
// the reason the cheap path was taken when it was not.
func (d Decision) Shout() string {
	var b strings.Builder

	switch d.Reason {
	case ReasonJev:
		fmt.Fprintf(&b, "jev %s (%.0f%%)", d.Move, d.Confidence*100)
	case ReasonJevFailed:
		fmt.Fprintf(&b, "jev timed out, code says %s", d.Move)
	case ReasonOnlyMove:
		fmt.Fprintf(&b, "%s is the only way out", d.Move)
	case ReasonTrapped:
		fmt.Fprintf(&b, "boxed in, trying %s", d.Move)
	case ReasonClearWinner:
		fmt.Fprintf(&b, "code %s, clearly best", d.Move)
	case ReasonInterchangeable:
		fmt.Fprintf(&b, "code %s, nothing to choose between them", d.Move)
	case ReasonNoBudget, ReasonColdPool, ReasonTieBreakDisabled:
		fmt.Fprintf(&b, "code %s, no time to ask", d.Move)
	default:
		fmt.Fprintf(&b, "code %s", d.Move)
	}

	if d.Mode != "" {
		fmt.Fprintf(&b, " · %s", d.Mode)
	}
	if c, ok := chosen(d); ok {
		fmt.Fprintf(&b, " · %d free", c.Space)
		if !c.TailSafe {
			b.WriteString(", tail cut off")
		}
		if c.H2H == game.H2HTie {
			fmt.Fprintf(&b, ", %d rival(s) could trade", c.Contesters)
		}
	}

	out := b.String()
	if len(out) > maxShout {
		out = out[:maxShout]
	}
	return out
}

// chosen finds the candidate that was actually played.
func chosen(d Decision) (Candidate, bool) {
	for _, c := range d.Candidates {
		if c.Dir.String() == d.Move {
			return c, true
		}
	}
	return Candidate{}, false
}
