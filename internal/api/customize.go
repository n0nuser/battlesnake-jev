package api

import (
	"fmt"
	"math"
	"math/rand/v2"
)

// Heads and tails the Battlesnake board knows how to draw.
var (
	heads = []string{
		"default", "beluga", "bendr", "evil", "fang", "pixel", "safe",
		"sand-worm", "shades", "silly", "smile", "tongue",
	}
	tails = []string{
		"default", "block-bum", "bolt", "curled", "fat-rattle", "freckled",
		"hook", "pixel", "round-bum", "sharp", "skinny", "small-rattle",
	}
)

// goldenAngle spaces successive hues about as far apart as hues can be spaced.
const goldenAngle = 137.508

// hueJitter is how far a hue may drift from its seeded position, in degrees.
const hueJitter = 8

// RandomCustomization picks a look for a snake, spreading the colour by seed.
//
// Several of these servers play in the same match, so a freely random hue is
// not enough: four independent draws land on neighbouring colours often enough
// to make a match hard to follow. Stepping the hue by the golden angle per seed
// - the listen port, in practice - keeps snakes far apart on the colour wheel,
// while a jitter and a random head and tail keep them from looking identical
// run to run.
func RandomCustomization(seed int) (color, head, tail string) {
	// The jitter stays well under the closest spacing the golden angle gives
	// four consecutive seeds (about 52 degrees), so run-to-run variation never
	// costs two snakes their separation.
	hue := math.Mod(float64(seed)*goldenAngle+rand.Float64()*hueJitter, 360)
	return hslToHex(hue, 0.72, 0.55),
		heads[rand.IntN(len(heads))],
		tails[rand.IntN(len(tails))]
}

// hslToHex converts a hue in degrees plus saturation and lightness in [0,1] to
// the hex string the API expects.
func hslToHex(h, s, l float64) string {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2

	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}

	to8 := func(v float64) int { return int(math.Round((v + m) * 255)) }
	return fmt.Sprintf("#%02X%02X%02X", to8(r), to8(g), to8(b))
}
