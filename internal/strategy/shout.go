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
	case ReasonJevAvoid:
		fmt.Fprintf(&b, "jev: not %s (%.0f%%), going %s", d.Avoided, d.Confidence*100, d.Move)
	case ReasonJevAlarm:
		fmt.Fprintf(&b, "jev: %s, playing %s safe", alarmWord(d.Advice), d.Move)
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
	if c, ok := d.Chosen(); ok {
		fmt.Fprintf(&b, " · %d free", c.Space)
		if !c.TailSafe {
			b.WriteString(", tail cut off")
		}
		if c.H2H == game.H2HTie {
			fmt.Fprintf(&b, ", %d rival(s) could trade", c.Contesters)
		}
	}

	if d.Advice.Present && d.Reason != ReasonJevAlarm {
		if w := strongestWarning(d.Advice); w != "" {
			b.WriteString(" · " + w)
		}
	}

	out := b.String()
	if len(out) > maxShout {
		out = out[:maxShout]
	}
	return out
}

// alarmWord describes how tight the model reads the position, for the board.
func alarmWord(a Advice) string {
	return fmt.Sprintf("room %.0f%% gone", a.Confinement()*100)
}

// strongestWarning reports a reading worth showing even when it did not fire.
func strongestWarning(a Advice) string {
	if c := a.Confinement(); c >= 0.35 {
		return fmt.Sprintf("room %.0f%% gone", c*100)
	}
	return ""
}
