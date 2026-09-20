package api

import (
	"fmt"
	"math"
	"regexp"
	"testing"
)

var hexColor = regexp.MustCompile(`^#[0-9A-F]{6}$`)

func TestRandomCustomizationIsWellFormed(t *testing.T) {
	headSet := toSet(heads)
	tailSet := toSet(tails)

	for range 200 {
		color, head, tail := RandomCustomization(8080)
		if !hexColor.MatchString(color) {
			t.Fatalf("colour %q is not a six-digit hex colour", color)
		}
		if !headSet[head] {
			t.Fatalf("head %q is not one the board can draw", head)
		}
		if !tailSet[tail] {
			t.Fatalf("tail %q is not one the board can draw", tail)
		}
	}
}

func TestRandomCustomizationVaries(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		color, _, _ := RandomCustomization(8080)
		seen[color] = true
	}
	// Fifty draws landing on one colour would mean the jitter is not running.
	if len(seen) < 10 {
		t.Errorf("only %d distinct colours in 50 draws", len(seen))
	}
}

// TestSeparateSeedsGiveSeparateHues is the point of seeding by port: four
// snakes in one match must be told apart at a glance.
func TestSeparateSeedsGiveSeparateHues(t *testing.T) {
	for range 100 {
		var hues []float64
		for _, port := range []int{8080, 8081, 8082, 8083} {
			color, _, _ := RandomCustomization(port)
			hues = append(hues, hueOf(t, color))
		}
		for i := range hues {
			for j := i + 1; j < len(hues); j++ {
				if d := hueGap(hues[i], hues[j]); d < 40 {
					t.Fatalf("hues %.0f and %.0f are only %.0f degrees apart", hues[i], hues[j], d)
				}
			}
		}
	}
}

func hueGap(a, b float64) float64 {
	d := math.Abs(a - b)
	if d > 180 {
		d = 360 - d
	}
	return d
}

// hueOf recovers the hue from a hex colour so the spacing can be checked.
func hueOf(t *testing.T, hex string) float64 {
	t.Helper()
	var r, g, b int
	if _, err := fmt.Sscanf(hex, "#%02X%02X%02X", &r, &g, &b); err != nil {
		t.Fatalf("parse %q: %v", hex, err)
	}
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	maxv := math.Max(rf, math.Max(gf, bf))
	minv := math.Min(rf, math.Min(gf, bf))
	d := maxv - minv
	if d == 0 {
		return 0
	}
	var h float64
	switch maxv {
	case rf:
		h = math.Mod((gf-bf)/d, 6)
	case gf:
		h = (bf-rf)/d + 2
	default:
		h = (rf-gf)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h
}

func TestHSLToHexKnownValues(t *testing.T) {
	tests := []struct {
		name    string
		h, s, l float64
		want    string
	}{
		{"black", 0, 0, 0, "#000000"},
		{"white", 0, 0, 1, "#FFFFFF"},
		{"red", 0, 1, 0.5, "#FF0000"},
		{"green", 120, 1, 0.5, "#00FF00"},
		{"blue", 240, 1, 0.5, "#0000FF"},
		{"cyan", 180, 1, 0.5, "#00FFFF"},
		{"magenta", 300, 1, 0.5, "#FF00FF"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hslToHex(tc.h, tc.s, tc.l); got != tc.want {
				t.Errorf("hslToHex(%g, %g, %g) = %s, want %s", tc.h, tc.s, tc.l, got, tc.want)
			}
		})
	}
}

func toSet(ss []string) map[string]bool {
	out := make(map[string]bool, len(ss))
	for _, s := range ss {
		out[s] = true
	}
	return out
}
