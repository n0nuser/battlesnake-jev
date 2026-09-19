package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/n0nuser/battlesnake-jev/internal/api"
	"github.com/n0nuser/battlesnake-jev/internal/strategy"
)

// exampleMoveRequest is the request from https://docs.battlesnake.com/api/example-move
// kept verbatim, so a change to our wire types that breaks the real engine
// breaks this test first.
const exampleMoveRequest = `{
  "game": {
    "id": "totally-unique-game-id",
    "ruleset": {"name": "standard", "version": "v1.1.15",
      "settings": {"foodSpawnChance": 15, "minimumFood": 1, "hazardDamagePerTurn": 14}},
    "map": "standard",
    "source": "league",
    "timeout": 500
  },
  "turn": 14,
  "board": {
    "height": 11, "width": 11,
    "food": [{"x": 5, "y": 5}, {"x": 9, "y": 0}, {"x": 2, "y": 6}],
    "hazards": [{"x": 3, "y": 2}],
    "snakes": [
      {"id": "snake-508e96ac-94ad-11ea-bb37", "name": "My Snake", "health": 54,
       "body": [{"x": 0, "y": 0}, {"x": 1, "y": 0}, {"x": 2, "y": 0}],
       "latency": "111", "head": {"x": 0, "y": 0}, "length": 3,
       "shout": "why are we shouting??",
       "customizations": {"color": "#FF0000", "head": "pixel", "tail": "pixel"}},
      {"id": "snake-b67f4906-94ae-11ea-bb37", "name": "Another Snake", "health": 16,
       "body": [{"x": 5, "y": 4}, {"x": 5, "y": 3}, {"x": 6, "y": 3}, {"x": 6, "y": 2}],
       "latency": "222", "head": {"x": 5, "y": 4}, "length": 4,
       "shout": "I'm not really sure...",
       "customizations": {"color": "#26CF04", "head": "silly", "tail": "curled"}}
    ]
  },
  "you": {"id": "snake-508e96ac-94ad-11ea-bb37", "name": "My Snake", "health": 54,
    "body": [{"x": 0, "y": 0}, {"x": 1, "y": 0}, {"x": 2, "y": 0}],
    "latency": "111", "head": {"x": 0, "y": 0}, "length": 3,
    "shout": "why are we shouting??",
    "customizations": {"color": "#FF0000", "head": "pixel", "tail": "pixel"}}
}`

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestHandler() *Handler {
	log := quietLogger()
	return New(
		api.InfoResponse{APIVersion: "1", Author: "tester", Color: "#4B8BBE"},
		strategy.NewStore(time.Hour),
		strategy.NewDecider(nil, strategy.DefaultConfig(), log),
		nil,
		log,
	)
}

func TestExampleMoveRequestParsesAndYieldsALegalMove(t *testing.T) {
	srv := httptest.NewServer(newTestHandler().Routes())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/move", "application/json", strings.NewReader(exampleMoveRequest))
	if err != nil {
		t.Fatalf("POST /move: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var got api.MoveResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// The example snake's head is at (0,0) with its body running right, so the
	// only survivable move is up.
	if got.Move != "up" {
		t.Errorf("move = %q, want \"up\" (the only safe move from the example board)", got.Move)
	}
}

func TestExampleRequestFieldsSurviveDecoding(t *testing.T) {
	var req api.GameRequest
	if err := json.Unmarshal([]byte(exampleMoveRequest), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"game id", req.Game.ID, "totally-unique-game-id"},
		{"timeout", req.Game.Timeout, 500},
		{"ruleset name", req.Game.Ruleset.Name, "standard"},
		{"hazard damage", req.Game.Ruleset.Settings.HazardDamagePerTurn, 14},
		{"turn", req.Turn, 14},
		{"board width", req.Board.Width, 11},
		{"food count", len(req.Board.Food), 3},
		{"hazard count", len(req.Board.Hazards), 1},
		{"snake count", len(req.Board.Snakes), 2},
		{"our health", req.You.Health, 54},
		{"our length", req.You.Length, 3},
		{"our head", req.You.Head, api.Coord{X: 0, Y: 0}},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestInfoEndpoint(t *testing.T) {
	srv := httptest.NewServer(newTestHandler().Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var got api.InfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.APIVersion != "1" {
		t.Errorf("apiversion = %q, want \"1\"", got.APIVersion)
	}
}

func TestStartAndEndTrackGameState(t *testing.T) {
	store := strategy.NewStore(time.Hour)
	log := quietLogger()
	h := New(api.InfoResponse{APIVersion: "1"}, store,
		strategy.NewDecider(nil, strategy.DefaultConfig(), log), nil, log)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	post := func(path string) int {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(exampleMoveRequest))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	if code := post("/start"); code != http.StatusOK {
		t.Errorf("POST /start = %d, want 200", code)
	}
	if store.Len() != 1 {
		t.Errorf("live games after /start = %d, want 1", store.Len())
	}
	if code := post("/end"); code != http.StatusOK {
		t.Errorf("POST /end = %d, want 200", code)
	}
	if store.Len() != 0 {
		t.Errorf("live games after /end = %d, want 0", store.Len())
	}
}

func TestMalformedBodyIsRejected(t *testing.T) {
	srv := httptest.NewServer(newTestHandler().Routes())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/move", "application/json", strings.NewReader("{not json"))
	if err != nil {
		t.Fatalf("POST /move: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestMoveRespectsTheGameTimeout uses a deliberately tiny timeout in the body:
// the handler must derive its deadline from that field, not from a constant.
func TestMoveRespectsTheGameTimeout(t *testing.T) {
	srv := httptest.NewServer(newTestHandler().Routes())
	defer srv.Close()

	body := strings.Replace(exampleMoveRequest, `"timeout": 500`, `"timeout": 50`, 1)

	start := time.Now()
	resp, err := http.Post(srv.URL+"/move", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /move: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("took %v, which blows a 50ms turn budget", elapsed)
	}
}

// TestConcurrentGames covers the shared-state hazard: several games hit the
// same server at once and must not interfere.
func TestConcurrentGames(t *testing.T) {
	store := strategy.NewStore(time.Hour)
	log := quietLogger()
	h := New(api.InfoResponse{APIVersion: "1"}, store,
		strategy.NewDecider(nil, strategy.DefaultConfig(), log), nil, log)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	const games = 8
	done := make(chan string, games)
	for i := range games {
		go func(n int) {
			body := strings.ReplaceAll(exampleMoveRequest,
				"totally-unique-game-id", "game-"+strconv.Itoa(n))
			resp, err := http.Post(srv.URL+"/move", "application/json", strings.NewReader(body))
			if err != nil {
				done <- ""
				return
			}
			defer func() { _ = resp.Body.Close() }()
			var out api.MoveResponse
			_ = json.NewDecoder(resp.Body).Decode(&out)
			done <- out.Move
		}(i)
	}
	for range games {
		if move := <-done; move != "up" {
			t.Errorf("concurrent game returned %q, want \"up\"", move)
		}
	}
	if store.Len() != games {
		t.Errorf("live games = %d, want %d", store.Len(), games)
	}
}
