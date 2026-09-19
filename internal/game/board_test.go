package game

import (
	"testing"

	"github.com/n0nuser/battlesnake-jev/internal/api"
)

func coord(x, y int) api.Coord { return api.Coord{X: x, Y: y} }

// snake builds a snake whose head is the first coordinate given.
func snake(id string, health int, body ...api.Coord) api.Battlesnake {
	return api.Battlesnake{
		ID:     id,
		Health: health,
		Body:   body,
		Head:   body[0],
		Length: len(body),
	}
}

func board(w, h int, snakes ...api.Battlesnake) api.Board {
	return api.Board{Width: w, Height: h, Snakes: snakes}
}

func TestDirectionApply(t *testing.T) {
	origin := coord(5, 5)
	tests := []struct {
		dir  Direction
		name string
		want api.Coord
	}{
		{Up, "up", coord(5, 6)},
		{Down, "down", coord(5, 4)},
		{Left, "left", coord(4, 5)},
		{Right, "right", coord(6, 5)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.dir.Apply(origin); got != tc.want {
				t.Errorf("Apply() = %v, want %v", got, tc.want)
			}
			if got := tc.dir.String(); got != tc.name {
				t.Errorf("String() = %q, want %q", got, tc.name)
			}
		})
	}
}

func TestOccupancyTailRelease(t *testing.T) {
	tests := []struct {
		name        string
		snake       api.Battlesnake
		tail        api.Coord
		wantBlocked bool
	}{
		{
			name:        "moving tail is released",
			snake:       snake("a", 90, coord(1, 1), coord(1, 2), coord(1, 3)),
			tail:        coord(1, 3),
			wantBlocked: false,
		},
		{
			name:        "full health means it just ate, so the tail is held",
			snake:       snake("a", 100, coord(1, 1), coord(1, 2), coord(1, 3)),
			tail:        coord(1, 3),
			wantBlocked: true,
		},
		{
			name:        "duplicated tail after eating stays blocked",
			snake:       snake("a", 90, coord(1, 1), coord(1, 2), coord(1, 3), coord(1, 3)),
			tail:        coord(1, 3),
			wantBlocked: true,
		},
		{
			name:        "snake stacked on its starting square stays blocked",
			snake:       snake("a", 90, coord(4, 4), coord(4, 4), coord(4, 4)),
			tail:        coord(4, 4),
			wantBlocked: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := Occupancy(board(11, 11, tc.snake))
			if got := g.Blocked(tc.tail); got != tc.wantBlocked {
				t.Errorf("Blocked(%v) = %v, want %v", tc.tail, got, tc.wantBlocked)
			}
			if !g.Blocked(tc.snake.Head) {
				t.Error("head square should always be blocked")
			}
		})
	}
}

func TestOccupancyTailHeldByAnotherSnake(t *testing.T) {
	// Our tail is about to move off (4,4), but another snake's mid-body sits
	// there, so the square stays blocked. Only that snake's own tail (3,4) is
	// released.
	mine := snake("a", 90, coord(4, 2), coord(4, 3), coord(4, 4))
	other := snake("b", 90, coord(6, 4), coord(5, 4), coord(4, 4), coord(3, 4))
	g := Occupancy(board(11, 11, mine, other))
	if !g.Blocked(coord(4, 4)) {
		t.Error("square shared with another snake's body must stay blocked")
	}
	if g.Blocked(coord(3, 4)) {
		t.Error("the other snake's own tail should still be released")
	}
}

func TestGridBounds(t *testing.T) {
	g := NewGrid(11, 11)
	for _, c := range []api.Coord{coord(-1, 0), coord(0, -1), coord(11, 0), coord(0, 11)} {
		if g.InBounds(c) {
			t.Errorf("InBounds(%v) = true, want false", c)
		}
		if !g.Blocked(c) {
			t.Errorf("Blocked(%v) = false, want true for off-board square", c)
		}
	}
	if !g.InBounds(coord(10, 10)) {
		t.Error("InBounds(10,10) = false on an 11x11 board")
	}
}

func TestFloodFill(t *testing.T) {
	t.Run("empty board is fully reachable", func(t *testing.T) {
		g := NewGrid(5, 5)
		if got := FloodFill(g, coord(0, 0)); got != 25 {
			t.Errorf("FloodFill() = %d, want 25", got)
		}
	})

	t.Run("blocked start has no space", func(t *testing.T) {
		b := board(5, 5, snake("a", 90, coord(2, 2), coord(2, 1)))
		g := Occupancy(b)
		if got := FloodFill(g, coord(2, 2)); got != 0 {
			t.Errorf("FloodFill() = %d, want 0", got)
		}
	})

	t.Run("wall seals a pocket", func(t *testing.T) {
		// A vertical wall on x=1 splits a 5x5 board into a 1-wide column and
		// the rest. The wall snake is at full health so its tail is held.
		wall := snake("wall", 100,
			coord(1, 0), coord(1, 1), coord(1, 2), coord(1, 3), coord(1, 4))
		g := Occupancy(board(5, 5, wall))
		if got := FloodFill(g, coord(0, 0)); got != 5 {
			t.Errorf("pocket size = %d, want 5", got)
		}
		if got := FloodFill(g, coord(4, 0)); got != 15 {
			t.Errorf("open side size = %d, want 15", got)
		}
	})
}

func TestSafeMoves(t *testing.T) {
	tests := []struct {
		name  string
		board api.Board
		you   api.Battlesnake
		want  []string
	}{
		{
			name:  "corner limits us to two moves",
			board: board(11, 11, snake("a", 90, coord(0, 0), coord(1, 0), coord(2, 0))),
			you:   snake("a", 90, coord(0, 0), coord(1, 0), coord(2, 0)),
			want:  []string{"up"},
		},
		{
			name:  "open board allows every move",
			board: board(11, 11, snake("a", 90, coord(5, 5), coord(5, 5), coord(5, 5))),
			you:   snake("a", 90, coord(5, 5), coord(5, 5), coord(5, 5)),
			want:  []string{"up", "down", "left", "right"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := Occupancy(tc.board)
			got := SafeMoves(g, tc.you)
			if len(got) != len(tc.want) {
				t.Fatalf("SafeMoves() = %v, want %v", names(got), tc.want)
			}
			for i, d := range got {
				if d.String() != tc.want[i] {
					t.Errorf("SafeMoves()[%d] = %q, want %q", i, d, tc.want[i])
				}
			}
		})
	}
}

// TestSafeMovesCornerRight covers the case where moving right is blocked by our
// own neck, which is the most common way a naive snake kills itself.
func TestSafeMovesCornerRight(t *testing.T) {
	you := snake("a", 90, coord(0, 0), coord(1, 0), coord(2, 0))
	g := Occupancy(board(11, 11, you))
	for _, d := range SafeMoves(g, you) {
		if d == Right {
			t.Error("moving right walks into our own neck")
		}
	}
}

func TestHeadToHeadRisk(t *testing.T) {
	me := snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3))
	target := coord(6, 5)

	tests := []struct {
		name     string
		opponent api.Battlesnake
		want     H2HRisk
	}{
		{
			name:     "longer opponent adjacent means we lose",
			opponent: snake("o", 90, coord(7, 5), coord(8, 5), coord(9, 5), coord(9, 6)),
			want:     H2HLose,
		},
		{
			name:     "equal length adjacent means both die",
			opponent: snake("o", 90, coord(7, 5), coord(8, 5), coord(9, 5)),
			want:     H2HTie,
		},
		{
			name:     "shorter opponent adjacent means we win",
			opponent: snake("o", 90, coord(7, 5), coord(8, 5)),
			want:     H2HWin,
		},
		{
			name:     "distant opponent is no risk",
			opponent: snake("o", 90, coord(0, 0), coord(0, 1), coord(0, 2), coord(0, 3)),
			want:     H2HNone,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := board(11, 11, me, tc.opponent)
			if got := HeadToHeadRisk(target, b, me); got != tc.want {
				t.Errorf("HeadToHeadRisk() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestHeadToHeadRiskIgnoresOurself(t *testing.T) {
	me := snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3))
	if got := HeadToHeadRisk(coord(6, 5), board(11, 11, me), me); got != H2HNone {
		t.Errorf("HeadToHeadRisk() = %d, want H2HNone", got)
	}
}

func TestHeadToHeadRiskWorstWins(t *testing.T) {
	me := snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3))
	shorter := snake("s", 90, coord(6, 6), coord(6, 7))
	longer := snake("l", 90, coord(7, 5), coord(8, 5), coord(9, 5), coord(9, 6))
	b := board(11, 11, me, shorter, longer)
	if got := HeadToHeadRisk(coord(6, 5), b, me); got != H2HLose {
		t.Errorf("HeadToHeadRisk() = %d, want H2HLose when any opponent is longer", got)
	}
}

func TestNearestFoodDistance(t *testing.T) {
	head := coord(5, 5)
	food := []api.Coord{coord(5, 9), coord(7, 5), coord(0, 0)}
	got, ok := NearestFoodDistance(head, food)
	if !ok || got != 2 {
		t.Errorf("NearestFoodDistance() = (%d, %v), want (2, true)", got, ok)
	}
	if _, ok := NearestFoodDistance(head, nil); ok {
		t.Error("NearestFoodDistance() reported food on an empty board")
	}
}

func TestHazardAt(t *testing.T) {
	hazards := []api.Coord{coord(3, 2), coord(4, 2)}
	if !HazardAt(coord(3, 2), hazards) {
		t.Error("HazardAt() missed a hazard square")
	}
	if HazardAt(coord(0, 0), hazards) {
		t.Error("HazardAt() reported a hazard on a clear square")
	}
}

func names(ds []Direction) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.String()
	}
	return out
}

func TestFloodReachFindsTheTail(t *testing.T) {
	// A 3-long snake in the open: from any neighbouring square its own tail is
	// still reachable, because the tail square frees up as it moves.
	you := snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3))
	g := Occupancy(board(11, 11, you))
	tail := coord(5, 3)

	if _, reaches := FloodReach(g, coord(4, 5), tail); !reaches {
		t.Error("tail should be reachable from an adjacent open square")
	}
}

func TestFloodReachReportsACutOffTail(t *testing.T) {
	// A hook of wall seals a 2x2 pocket in the bottom-left corner. Our snake
	// sits inside it, so nothing outside the pocket can be reached.
	you := snake("me", 100, coord(1, 1), coord(1, 0))
	wall := snake("wall", 100,
		coord(2, 0), coord(2, 1), coord(2, 2), coord(1, 2), coord(0, 2))
	g := Occupancy(board(11, 11, you, wall))

	size, reaches := FloodReach(g, coord(0, 1), coord(5, 5))
	if reaches {
		t.Error("a square outside the sealed pocket should be unreachable")
	}
	if size != 2 {
		t.Errorf("pocket size = %d, want 2 (the two free corner squares)", size)
	}
	if _, reaches := FloodReach(g, coord(0, 1), coord(1, 1)); !reaches {
		t.Error("a square inside the pocket should be reachable")
	}
}

func TestFloodReachOnABlockedStart(t *testing.T) {
	you := snake("me", 90, coord(5, 5), coord(5, 4), coord(5, 3))
	g := Occupancy(board(11, 11, you))
	if size, reaches := FloodReach(g, coord(5, 5), coord(5, 3)); size != 0 || reaches {
		t.Errorf("FloodReach() from a blocked square = (%d, %v), want (0, false)", size, reaches)
	}
}

func TestEdgeDistance(t *testing.T) {
	g := NewGrid(11, 11)
	tests := []struct {
		name string
		c    api.Coord
		want int
	}{
		{"corner", coord(0, 0), 0},
		{"far corner", coord(10, 10), 0},
		{"centre", coord(5, 5), 5},
		{"one in from the left", coord(1, 5), 1},
		{"one in from the top", coord(5, 9), 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EdgeDistance(g, tc.c); got != tc.want {
				t.Errorf("EdgeDistance(%v) = %d, want %d", tc.c, got, tc.want)
			}
		})
	}
}
