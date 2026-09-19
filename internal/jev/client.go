// Package jev is a small client for the TypeSafe System One API.
//
// TypeSafe publishes Python and JavaScript SDKs but no Go SDK, so this is
// hand-written against the contract at https://docs.typesafe.ai/api.md .
//
// Latency is the reason this package exists in the shape it does. Measured
// against api.typesafe.ai, a warm call on a reused connection takes roughly
// 300ms while a cold one pays a TLS handshake and takes 640ms or more. A
// Battlesnake turn budget is 500ms including round-trip, so the connection pool
// must stay warm and a call that cannot be afforded must not be made at all.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"
)

// DefaultEndpoint is the System One inference endpoint.
const DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"

// DefaultModel is the stable alias; the response echoes the versioned id.
const DefaultModel = "jev-latest"

// Question types supported by the API.
const (
	TypeChoice = "choice"
	TypeNoul   = "noul"
	TypeScore  = "score"
)

// Question is a single typed judgment. Criteria is a map of option name to
// description for a choice, an ordered slice of level descriptions for a score,
// and optional for a noul.
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Request is the body posted to the inference endpoint.
type Request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Answer is one typed judgment. Which fields are populated depends on Type.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// Usage reports billed tokens. Input tokens are the number this project tries
// to keep small; output tokens are free.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is the decoded reply, plus locally observed call metadata.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`

	// Latency is the wall time the call took.
	Latency time.Duration `json:"-"`
	// ConnReused reports whether an existing connection was used. A false
	// value means a TLS handshake was paid for on this call.
	ConnReused bool `json:"-"`
}

// APIError is a non-2xx reply from the service.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("typesafe: http %d: %s", e.StatusCode, e.Body)
}

// Retryable reports whether backing off and trying again could succeed. Only
// the background path should act on this; an in-turn call has no time to retry.
func (e *APIError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode == 529
}

// Asker is the behaviour callers depend on, so strategy code can be tested
// against a fake with no network.
type Asker interface {
	Ask(ctx context.Context, req Request) (*Response, error)
}

// Client talks to the TypeSafe API over a pooled, kept-alive connection.
type Client struct {
	endpoint string
	model    string
	apiKey   string
	http     *http.Client

	// lastSuccess is the unix nano of the last completed call, used to guess
	// whether the pooled connection is still warm.
	lastSuccess atomic.Int64
	// coldAfter is how long an idle pool is assumed to have gone cold.
	coldAfter time.Duration

	inputTokens atomic.Int64
	calls       atomic.Int64
}

// Option configures a Client.
type Option func(*Client)

// WithEndpoint overrides the API endpoint, which tests use to point at a stub.
func WithEndpoint(url string) Option { return func(c *Client) { c.endpoint = url } }

// WithModel overrides the model alias.
func WithModel(m string) Option { return func(c *Client) { c.model = m } }

// WithColdAfter sets how long an idle connection is assumed to stay warm.
func WithColdAfter(d time.Duration) Option { return func(c *Client) { c.coldAfter = d } }

// New builds a client whose transport is tuned to keep one connection alive for
// the whole of a game, because a cold connect costs more than a turn budget.
func New(apiKey string, opts ...Option) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 8
	transport.MaxIdleConnsPerHost = 4
	transport.MaxConnsPerHost = 8
	// Minutes, not seconds: a stretch of turns that skip inference must not
	// let the pool drop the connection and hand a handshake to a later turn.
	transport.IdleConnTimeout = 10 * time.Minute
	transport.ForceAttemptHTTP2 = true

	c := &Client{
		endpoint:  DefaultEndpoint,
		model:     DefaultModel,
		apiKey:    apiKey,
		coldAfter: 4 * time.Minute,
		http:      &http.Client{Transport: transport},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// LikelyCold reports whether the pooled connection has probably been dropped,
// which means the next call would pay a handshake it cannot afford in-turn.
// Callers should skip inference for that turn and warm the pool in background.
func (c *Client) LikelyCold() bool {
	last := c.lastSuccess.Load()
	if last == 0 {
		return true
	}
	return time.Since(time.Unix(0, last)) > c.coldAfter
}

// Stats returns the number of calls made and input tokens spent so far.
func (c *Client) Stats() (calls, inputTokens int64) {
	return c.calls.Load(), c.inputTokens.Load()
}

// Ask performs one inference call. It never retries: callers that can afford a
// retry should use AskWithRetry, and callers on a turn deadline cannot.
func (c *Client) Ask(ctx context.Context, req Request) (*Response, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("typesafe: encode request: %w", err)
	}

	var reused bool
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
	}
	ctx = httptrace.WithClientTrace(ctx, trace)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("typesafe: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	start := time.Now()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("typesafe: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Drain and close so the connection returns to the pool and stays warm.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("typesafe: read response: %w", err)
	}
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(raw)}
	}

	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("typesafe: decode response: %w", err)
	}
	out.Latency = elapsed
	out.ConnReused = reused

	c.lastSuccess.Store(time.Now().UnixNano())
	c.calls.Add(1)
	c.inputTokens.Add(int64(out.Usage.InputTokens))
	return &out, nil
}

// AskWithRetry backs off on 429 and 529. It is for background calls only.
func (c *Client) AskWithRetry(ctx context.Context, req Request, attempts int) (*Response, error) {
	var lastErr error
	backoff := 200 * time.Millisecond
	for i := range attempts {
		resp, err := c.Ask(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var apiErr *APIError
		if !asAPIError(err, &apiErr) || !apiErr.Retryable() {
			return nil, err
		}
		if i == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}
	return nil, lastErr
}

// Warm makes the cheapest useful call so the TLS handshake is paid off the
// critical path. Call it when a game starts and whenever the pool goes cold.
func (c *Client) Warm(ctx context.Context) error {
	_, err := c.Ask(ctx, Request{
		State: "warmup",
		Questions: map[string]Question{
			"w": {Type: TypeNoul, Instructions: "Is this a warmup request?"},
		},
	})
	return err
}
