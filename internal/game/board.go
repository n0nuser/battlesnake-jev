// Package game holds pure board logic for Battlesnake: no I/O, no context and
// no clock, so every function here is directly table-testable.
//
// Rules encoded here come from https://docs.battlesnake.com/rules :
// snakes lose 1 health per turn, food restores health to its maximum and costs
// nothing, growth lands the turn after eating, and a snake is eliminated when
// its health reaches 0, when it leaves the board, when it hits any body, or
// when it loses a head-to-head collision.
package game

import "github.com/n0nuser/battlesnake-jev/internal/api"

// maxHealth is the health a snake is restored to when it eats.
const maxHealth = 100

// Direction is one of the four moves a snake may make.
type Direction uint8

// The four legal moves. The board origin is bottom-left and y increases
// upward, so Up is y+1.
const (
	Up Direction = iota
	Down
	Left
	Right
)

// Directions lists every legal move in a stable order.
var Directions = [4]Direction{Up, Down, Left, Right}

// String returns the wire name the Battlesnake API expects.
func (d Direction) String() string {
	switch d {
	case Up:
		return "up"
	case Down:
		return "down"
	case Left:
		return "left"
	case Right:
		return "right"
	default:
		return "up"
	}
}

// Apply returns the coordinate reached by moving one square in d.
func (d Direction) Apply(c api.Coord) api.Coord {
	switch d {
	case Up:
		return api.Coord{X: c.X, Y: c.Y + 1}
	case Down:
		return api.Coord{X: c.X, Y: c.Y - 1}
	case Left:
		return api.Coord{X: c.X - 1, Y: c.Y}
	case Right:
		return api.Coord{X: c.X + 1, Y: c.Y}
	default:
		return c
	}
}

// Grid is an occupancy map of the board for the turn about to be played.
type Grid struct {
	Width  int
	Height int
	counts []int
}

// NewGrid returns an empty grid of the given size.
func NewGrid(width, height int) *Grid {
	return &Grid{Width: width, Height: height, counts: make([]int, width*height)}
}

// InBounds reports whether c lies on the board.
func (g *Grid) InBounds(c api.Coord) bool {
	return c.X >= 0 && c.Y >= 0 && c.X < g.Width && c.Y < g.Height
}

// Blocked reports whether c is off the board or occupied by a snake.
func (g *Grid) Blocked(c api.Coord) bool {
	if !g.InBounds(c) {
		return true
	}
	return g.counts[c.Y*g.Width+c.X] > 0
}

// Occupancy builds the grid of squares a snake cannot move into this turn.
//
// Every body segment blocks its square. A snake's tail is the one exception:
// it vacates its square as the snake moves, so it is released again unless the
// snake is growing into it.
//
// Two things make that release safe. Segments are counted rather than flagged,
// and only one count is released per tail, so a stacked snake - whose body
// holds the same coordinate more than once, either at the start of a game or
// on the turn after it eats, when the engine duplicates the tail - keeps its
// square blocked. On top of that a snake at full health has just eaten, so its
// tail is held as well. The second check is redundant against an engine that
// duplicates the tail, and it is kept because wrongly releasing a square kills
// the snake while wrongly holding one only costs a little reachable space.
func Occupancy(b api.Board) *Grid {
	g := NewGrid(b.Width, b.Height)
	for i := range b.Snakes {
		s := &b.Snakes[i]
		for _, c := range s.Body {
			if g.InBounds(c) {
				g.counts[c.Y*g.Width+c.X]++
			}
		}
	}
	for i := range b.Snakes {
		s := &b.Snakes[i]
		if len(s.Body) == 0 || s.Health >= maxHealth {
			continue
		}
		tail := s.Body[len(s.Body)-1]
		if g.InBounds(tail) {
			g.counts[tail.Y*g.Width+tail.X]--
		}
	}
	return g
}

// FloodFill counts the squares reachable from start, including start itself.
// It returns 0 when start is blocked. This is the primary survival signal: a
// move into a pocket smaller than the snake is a delayed self-collision.
func FloodFill(g *Grid, start api.Coord) int {
	size, _ := FloodReach(g, start, api.Coord{X: -1, Y: -1})
	return size
}

// FloodReach counts the squares reachable from start and reports whether
// target is among them.
//
// Reaching our own tail matters more than raw space. A snake that can still
// path to its tail can survive indefinitely by following it, because the tail
// keeps vacating squares ahead of the head. A large open area with no route
// back to the tail is how a snake walks into a trap several turns before the
// trap closes, which raw square-counting cannot see.
func FloodReach(g *Grid, start, target api.Coord) (size int, reachesTarget bool) {
	if g.Blocked(start) {
		return 0, false
	}
	seen := make([]bool, g.Width*g.Height)
	queue := make([]api.Coord, 0, g.Width*g.Height)
	queue = append(queue, start)
	seen[start.Y*g.Width+start.X] = true
	if start == target {
		reachesTarget = true
	}
	for len(queue) > 0 {
		c := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		size++
		for _, d := range Directions {
			n := d.Apply(c)
			if !g.InBounds(n) {
				continue
			}
			idx := n.Y*g.Width + n.X
			if seen[idx] {
				continue
			}
			if n == target {
				reachesTarget = true
			}
			if g.Blocked(n) {
				continue
			}
			seen[idx] = true
			queue = append(queue, n)
		}
	}
	return size, reachesTarget
}

// EdgeDistance returns how far c sits from the nearest wall. Squares in the
// open middle keep more escape routes than squares pinned against an edge.
func EdgeDistance(g *Grid, c api.Coord) int {
	d := c.X
	for _, v := range []int{c.Y, g.Width - 1 - c.X, g.Height - 1 - c.Y} {
		if v < d {
			d = v
		}
	}
	return d
}

// SafeMoves returns the directions that do not walk into a wall or a body.
// The result may be empty, which means every move loses; callers must still
// return a legal move.
func SafeMoves(g *Grid, you api.Battlesnake) []Direction {
	safe := make([]Direction, 0, len(Directions))
	for _, d := range Directions {
		if !g.Blocked(d.Apply(you.Head)) {
			safe = append(safe, d)
		}
	}
	return safe
}

// ManhattanDistance returns the step count between two squares ignoring bodies.
func ManhattanDistance(a, b api.Coord) int {
	return abs(a.X-b.X) + abs(a.Y-b.Y)
}

// NearestFoodDistance returns the distance to the closest food and whether any
// food exists.
func NearestFoodDistance(from api.Coord, food []api.Coord) (int, bool) {
	best := 0
	found := false
	for _, f := range food {
		d := ManhattanDistance(from, f)
		if !found || d < best {
			best, found = d, true
		}
	}
	return best, found
}

// HazardAt reports whether c is a hazard square, which drains extra health.
func HazardAt(c api.Coord, hazards []api.Coord) bool {
	for _, h := range hazards {
		if h == c {
			return true
		}
	}
	return false
}

// H2HRisk is the outcome of a possible head-to-head collision.
type H2HRisk uint8

// Head-to-head outcomes, ordered from best to worst so they can be compared.
const (
	// H2HNone means no opponent head can reach the square next turn.
	H2HNone H2HRisk = iota
	// H2HWin means every opponent that can reach it is strictly shorter.
	H2HWin
	// H2HTie means an equally long opponent can reach it; both would die.
	H2HTie
	// H2HLose means a longer opponent can reach it and we would die.
	H2HLose
)

// HeadToHeadRisk classifies moving into target against every other snake that
// could move into the same square. The longer snake survives a head-to-head
// and equal lengths eliminate both, so the worst outcome found is returned.
func HeadToHeadRisk(target api.Coord, b api.Board, you api.Battlesnake) H2HRisk {
	worst := H2HNone
	for i := range b.Snakes {
		s := &b.Snakes[i]
		if s.ID == you.ID {
			continue
		}
		if ManhattanDistance(s.Head, target) != 1 {
			continue
		}
		var risk H2HRisk
		switch {
		case s.Length > you.Length:
			risk = H2HLose
		case s.Length == you.Length:
			risk = H2HTie
		default:
			risk = H2HWin
		}
		if risk > worst {
			worst = risk
		}
	}
	return worst
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
