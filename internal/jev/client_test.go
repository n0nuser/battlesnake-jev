package jev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const canned = `{"model":"jev-1.13.0","answers":{"move":{"type":"choice","choice":"up",` +
	`"confidence":0.8,"probabilities":{"up":0.87,"down":0.05,"left":0.08}}},` +
	`"usage":{"input_tokens":484,"output_tokens":38}}`

func moveRequest() Request {
	return Request{
		State: map[string]any{"turn": 87},
		Questions: map[string]Question{
			"move": {Type: TypeChoice, Instructions: "pick one", Criteria: map[string]string{"up": "a", "down": "b"}},
		},
	}
}

func TestAskSendsCredentialsAndDecodesTheAnswer(t *testing.T) {
	var gotAuth, gotType string
	var gotBody Request

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(canned))
	}))
	defer srv.Close()

	c := New("test-key", WithEndpoint(srv.URL))
	resp, err := c.Ask(context.Background(), moveRequest())
	if err != nil {
		t.Fatalf("Ask() error: %v", err)
	}

	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-key")
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotType)
	}
	if gotBody.Model != DefaultModel {
		t.Errorf("model = %q, want %q", gotBody.Model, DefaultModel)
	}
	if resp.Answers["move"].Choice != "up" {
		t.Errorf("choice = %q, want \"up\"", resp.Answers["move"].Choice)
	}
	if resp.Usage.InputTokens != 484 {
		t.Errorf("input tokens = %d, want 484", resp.Usage.InputTokens)
	}
	if resp.Latency <= 0 {
		t.Error("Latency was not recorded")
	}

	calls, tokens := c.Stats()
	if calls != 1 || tokens != 484 {
		t.Errorf("Stats() = (%d, %d), want (1, 484)", calls, tokens)
	}
}

func TestAskSurfacesAPIErrors(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		wantRetryable bool
	}{
		{"unauthorised", http.StatusUnauthorized, false},
		{"validation failed", http.StatusUnprocessableEntity, false},
		{"rate limited", http.StatusTooManyRequests, true},
		{"overloaded", 529, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"detail":"nope"}`))
			}))
			defer srv.Close()

			c := New("k", WithEndpoint(srv.URL))
			_, err := c.Ask(context.Background(), moveRequest())

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want an *APIError", err)
			}
			if apiErr.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tc.status)
			}
			if apiErr.Retryable() != tc.wantRetryable {
				t.Errorf("Retryable() = %v, want %v", apiErr.Retryable(), tc.wantRetryable)
			}
		})
	}
}

func TestAskRespectsContextCancellation(t *testing.T) {
	// The handler holds the request open until the test releases it. The
	// release is deferred after Close so it runs first, otherwise Close would
	// wait forever on the in-flight request.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c := New("k", WithEndpoint(srv.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := c.Ask(ctx, moveRequest()); err == nil {
		t.Fatal("Ask() returned no error after the deadline passed")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Errorf("Ask() took %v to honour a 30ms deadline", elapsed)
	}
}

func TestAskWithRetryBacksOffThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(canned))
	}))
	defer srv.Close()

	c := New("k", WithEndpoint(srv.URL))
	resp, err := c.AskWithRetry(context.Background(), moveRequest(), 3)
	if err != nil {
		t.Fatalf("AskWithRetry() error: %v", err)
	}
	if resp.Answers["move"].Choice != "up" {
		t.Errorf("choice = %q, want \"up\"", resp.Answers["move"].Choice)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestAskWithRetryGivesUpOnPermanentErrors(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New("k", WithEndpoint(srv.URL))
	if _, err := c.AskWithRetry(context.Background(), moveRequest(), 3); err == nil {
		t.Fatal("AskWithRetry() succeeded against a 401")
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1: a 401 must not be retried", got)
	}
}

// TestLikelyColdGuardsTheHandshake covers the measured hazard: a cold connect
// costs more than a whole turn budget, so a call must be skipped instead.
func TestLikelyColdGuardsTheHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(canned))
	}))
	defer srv.Close()

	c := New("k", WithEndpoint(srv.URL), WithColdAfter(50*time.Millisecond))
	if !c.LikelyCold() {
		t.Error("a client that has never called should report itself cold")
	}

	if _, err := c.Ask(context.Background(), moveRequest()); err != nil {
		t.Fatalf("Ask() error: %v", err)
	}
	if c.LikelyCold() {
		t.Error("client reported cold immediately after a successful call")
	}

	time.Sleep(80 * time.Millisecond)
	if !c.LikelyCold() {
		t.Error("client did not report cold after the idle window elapsed")
	}
}

func TestWarmPaysTheHandshakeUpFront(t *testing.T) {
	var got Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"w":{"type":"noul","noul":0.9}},` +
			`"usage":{"input_tokens":20,"output_tokens":2}}`))
	}))
	defer srv.Close()

	c := New("k", WithEndpoint(srv.URL))
	if err := c.Warm(context.Background()); err != nil {
		t.Fatalf("Warm() error: %v", err)
	}
	if got.Questions["w"].Type != TypeNoul {
		t.Errorf("warm-up question type = %q, want %q", got.Questions["w"].Type, TypeNoul)
	}
	if c.LikelyCold() {
		t.Error("client still reports cold after warming")
	}
}

func TestMalformedResponseIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := New("k", WithEndpoint(srv.URL))
	if _, err := c.Ask(context.Background(), moveRequest()); err == nil {
		t.Fatal("Ask() accepted a malformed response")
	}
}

func TestClientSatisfiesAsker(t *testing.T) {
	var _ Asker = New("k")
}
