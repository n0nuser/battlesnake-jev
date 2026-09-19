// Command battlesnake serves a Battlesnake whose tactics are deterministic Go
// and whose strategy comes from the TypeSafe Jev model.
//
// Configuration is read from the environment:
//
//	PORT                 listen port (default 8080)
//	TYPESAFE_API_KEY     enables inference; without it play is fully
//	                     deterministic, which is a supported mode
//	JEV_TIEBREAK         set to "false" to keep inference to the background
//	                     posture only, the documented degraded mode
//	JEV_MARGIN_MS        slack held back from the turn budget (default 120)
//	LOG_LEVEL            debug, info, warn or error (default info)
//	SNAKE_AUTHOR, SNAKE_COLOR, SNAKE_HEAD, SNAKE_TAIL
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/n0nuser/battlesnake-jev/internal/api"
	"github.com/n0nuser/battlesnake-jev/internal/jev"
	"github.com/n0nuser/battlesnake-jev/internal/server"
	"github.com/n0nuser/battlesnake-jev/internal/strategy"
)

// gameTTL sweeps games that never sent /end, so state cannot leak.
const gameTTL = 30 * time.Minute

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	log := newLogger()
	slog.SetDefault(log)

	cfg := strategy.DefaultConfig()
	cfg.TieBreak = envBool("JEV_TIEBREAK", true)
	cfg.Margin = time.Duration(envInt("JEV_MARGIN_MS", 120)) * time.Millisecond

	var (
		asker  jev.Asker
		warmer server.Warmer
	)
	if key := os.Getenv("TYPESAFE_API_KEY"); key != "" {
		client := jev.New(key)
		asker, warmer = client, client
		log.Info("inference enabled",
			"model", jev.DefaultModel, "tiebreak", cfg.TieBreak, "margin", cfg.Margin)

		// Pay the TLS handshake before any game arrives. A cold connect costs
		// more than a whole turn budget, and waiting for the first /start
		// leaves the opening turns of a game unable to afford inference.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := client.Warm(ctx); err != nil {
				log.Warn("startup warm-up failed", "err", err)
				return
			}
			log.Info("inference connection warmed")
		}()
	} else {
		log.Warn("TYPESAFE_API_KEY is not set: playing deterministically")
	}

	info := api.InfoResponse{
		APIVersion: "1",
		Author:     envStr("SNAKE_AUTHOR", "n0nuser"),
		Color:      envStr("SNAKE_COLOR", "#4B8BBE"),
		Head:       envStr("SNAKE_HEAD", "smart-caterpillar"),
		Tail:       envStr("SNAKE_TAIL", "weight"),
		Version:    envStr("SNAKE_VERSION", "0.1.0"),
	}

	handler := server.New(info, strategy.NewStore(gameTTL), strategy.NewDecider(asker, cfg, log), warmer, log)

	addr := ":" + envStr("PORT", "8080")
	srv := &http.Server{
		Addr:    addr,
		Handler: handler.Routes(),
		// Explicit timeouts: an unbounded server will eventually hang.
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(envStr("LOG_LEVEL", "info"))); err != nil {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}
