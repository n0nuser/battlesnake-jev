package strategy

import (
	"strings"

	"github.com/n0nuser/battlesnake-jev/internal/api"
)

// Board glyphs. They are single characters so an 11x11 board costs only a few
// dozen tokens, and distinct enough that the grid reads at a glance.
const (
	glyphEmpty     = '.'
	glyphFood      = 'o'
	glyphHazard    = 'x'
	glyphMyHead    = 'H'
	glyphMyBody    = '#'
	glyphTheirHead = 'E'
	glyphTheirBody = '+'
)

// renderBoard draws the board as text, top row first so that up on the board is
// up in the drawing.
//
// The per-option labels tell the model what each move measures; this tells it
// what the position looks like. Shape is the thing a summary loses: a corridor,
// a rival curling around us, an opening about to close. Without it the model is
// re-ranking numbers the code already computed, which cannot beat the code.
func renderBoard(b api.Board, you api.Battlesnake) string {
	if b.Width <= 0 || b.Height <= 0 {
		return ""
	}
	cells := make([][]rune, b.Height)
	for y := range cells {
		cells[y] = make([]rune, b.Width)
		for x := range cells[y] {
			cells[y][x] = glyphEmpty
		}
	}
	put := func(c api.Coord, r rune) {
		if c.X >= 0 && c.Y >= 0 && c.X < b.Width && c.Y < b.Height {
			cells[c.Y][c.X] = r
		}
	}

	for _, f := range b.Food {
		put(f, glyphFood)
	}
	for _, h := range b.Hazards {
		put(h, glyphHazard)
	}
	for i := range b.Snakes {
		s := &b.Snakes[i]
		body, head := glyphTheirBody, glyphTheirHead
		if s.ID == you.ID {
			body, head = glyphMyBody, glyphMyHead
		}
		for _, c := range s.Body {
			put(c, body)
		}
		put(s.Head, head)
	}

	var sb strings.Builder
	sb.Grow(b.Height * (b.Width + 1))
	for y := b.Height - 1; y >= 0; y-- {
		sb.WriteString(string(cells[y]))
		if y > 0 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
