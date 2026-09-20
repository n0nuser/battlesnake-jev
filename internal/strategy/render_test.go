package strategy

import (
	"strings"
	"testing"

	"github.com/n0nuser/battlesnake-jev/internal/api"
)

func TestRenderBoardDrawsThePositionRightWayUp(t *testing.T) {
	me := snake("me", 90, coord(1, 1), coord(1, 0))
	rival := snake("rival", 90, coord(3, 3), coord(3, 4))
	b := api.Board{
		Width: 5, Height: 5,
		Snakes:  []api.Battlesnake{me, rival},
		Food:    []api.Coord{coord(4, 0)},
		Hazards: []api.Coord{coord(0, 4)},
	}

	got := renderBoard(b, me)
	want := strings.Join([]string{
		"x..+.", // y=4: hazard at (0,4), rival body at (3,4)
		"...E.", // y=3: rival head
		".....", // y=2
		".H...", // y=1: our head
		".#..o", // y=0: our body, food at (4,0)
	}, "\n")

	if got != want {
		t.Errorf("renderBoard() =\n%s\n\nwant\n%s", got, want)
	}
}

func TestRenderBoardIsCheap(t *testing.T) {
	var snakes []api.Battlesnake
	snakes = append(snakes, snake("me", 90, coord(5, 5), coord(5, 4)))
	b := api.Board{Width: 11, Height: 11, Snakes: snakes}

	got := renderBoard(b, snakes[0])
	if lines := strings.Count(got, "\n") + 1; lines != 11 {
		t.Errorf("rendered %d lines, want 11", lines)
	}
	// An 11x11 board is 121 glyphs plus 10 newlines: a few dozen tokens, which
	// is the whole reason drawing the board is affordable every turn.
	if len(got) != 131 {
		t.Errorf("rendered %d bytes, want 131", len(got))
	}
}

func TestRenderBoardHandlesAnEmptyBoard(t *testing.T) {
	if got := renderBoard(api.Board{}, api.Battlesnake{}); got != "" {
		t.Errorf("renderBoard() = %q on a zero-sized board, want empty", got)
	}
}

func TestBoardInStateIsOptIn(t *testing.T) {
	req := tradeOffRequest()
	cands := []Candidate{
		{Dir: 0, Space: 40, FoodDist: 5, HasFood: true, TailSafe: true},
		{Dir: 1, Space: 18, FoodDist: 7, HasFood: true, TailSafe: true},
	}

	off := tieBreakRequest(req, ModeSurvive, cands, false).State.(map[string]any)
	if _, present := off["board"]; present {
		t.Error("board was sent although it was not enabled")
	}

	on := tieBreakRequest(req, ModeSurvive, cands, true).State.(map[string]any)
	board, present := on["board"].(string)
	if !present {
		t.Fatal("board was not sent although it was enabled")
	}
	if !strings.Contains(board, "H") {
		t.Error("rendered board does not show our head")
	}
	if _, present := on["legend"]; !present {
		t.Error("board was sent without a legend explaining its glyphs")
	}
}
