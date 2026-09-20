// Package server exposes the four Battlesnake webhooks over HTTP.
//
// The engine's per-move budget includes the round trip to and from this
// process, so every handler derives its deadline from the game's own timeout
// field rather than assuming the usual 500ms.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/n0nuser/battlesnake-jev/internal/api"
	"github.com/n0nuser/battlesnake-jev/internal/strategy"
)

// defaultTimeout is used when a request omits the game timeout.
const defaultTimeout = 500 * time.Millisecond

// Warmer is implemented by an inference client that can pay its TLS handshake
// ahead of time. A cold connect costs more than a whole turn budget.
type Warmer interface {
	Warm(ctx context.Context) error
}

// Handler serves the Battlesnake webhooks.
type Handler struct {
	info    api.InfoResponse
	store   *strategy.Store
	decider *strategy.Decider
	warmer  Warmer
	log     *slog.Logger
}

// New builds a Handler. warmer may be nil when inference is disabled.
func New(info api.InfoResponse, store *strategy.Store, decider *strategy.Decider, warmer Warmer, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{info: info, store: store, decider: decider, warmer: warmer, log: log}
}

// Routes returns the mux serving the four webhooks.
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.handleInfo)
	mux.HandleFunc("POST /start", h.handleStart)
	mux.HandleFunc("POST /move", h.handleMove)
	mux.HandleFunc("POST /end", h.handleEnd)
	return mux
}

func (h *Handler) handleInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, h.info, h.log)
}

func (h *Handler) handleStart(w http.ResponseWriter, r *http.Request) {
	req, ok := decode(w, r, h.log)
	if !ok {
		return
	}
	h.store.Start(stateKey(req))
	h.log.Info("game started",
		"game", req.Game.ID, "ruleset", req.Game.Ruleset.Name,
		"map", req.Game.Map, "timeout_ms", req.Game.Timeout,
		"board", req.Board.Width, "snakes", len(req.Board.Snakes))

	// Pay the TLS handshake now so the first turn that needs inference does
	// not. Detached from the request context, which ends with this response.
	if h.warmer != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := h.warmer.Warm(ctx); err != nil {
				h.log.Warn("inference warm-up failed", "game", req.Game.ID, "err", err)
			}
		}()
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleMove(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, ok := decode(w, r, h.log)
	if !ok {
		return
	}

	timeout := defaultTimeout
	if req.Game.Timeout > 0 {
		timeout = time.Duration(req.Game.Timeout) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	gs := h.store.Get(stateKey(req))
	decision := h.decider.Decide(ctx, req, gs)

	writeJSON(w, api.MoveResponse{Move: decision.Move}, h.log)

	h.log.Debug("move",
		"game", req.Game.ID, "turn", req.Turn, "move", decision.Move,
		"reason", decision.Reason, "mode", decision.Mode,
		"health", req.You.Health, "length", req.You.Length,
		"tokens", decision.Tokens, "took", time.Since(start))
}

func (h *Handler) handleEnd(w http.ResponseWriter, r *http.Request) {
	req, ok := decode(w, r, h.log)
	if !ok {
		return
	}
	gs := h.store.End(stateKey(req))
	if gs != nil {
		calls, tokens, fallbacks, overrides := gs.Stats()
		h.log.Info("game ended",
			"game", req.Game.ID, "turns", req.Turn,
			"alive", isAlive(req),
			"inference_calls", calls, "input_tokens", tokens,
			"fallbacks", fallbacks, "overrides", overrides,
			"final_mode", gs.Mode())
	}
	w.WriteHeader(http.StatusOK)
}

// stateKey identifies one snake in one game.
//
// The game id alone is not enough: this server can be the backend for several
// snakes in the same match, which is how a one-against-three game is set up.
// Keying on the game alone would make those snakes share a posture, a latency
// estimate and a token count.
func stateKey(req api.GameRequest) string {
	return req.Game.ID + "/" + req.You.ID
}

// isAlive reports whether we were still on the board when the game ended.
func isAlive(req api.GameRequest) bool {
	for i := range req.Board.Snakes {
		if req.Board.Snakes[i].ID == req.You.ID {
			return true
		}
	}
	return false
}

func decode(w http.ResponseWriter, r *http.Request, log *slog.Logger) (api.GameRequest, bool) {
	var req api.GameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Error("bad request body", "path", r.URL.Path, "err", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return api.GameRequest{}, false
	}
	return req, true
}

func writeJSON(w http.ResponseWriter, v any, log *slog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already written, so this can only be logged.
		log.Error("write response", "err", err)
	}
}
