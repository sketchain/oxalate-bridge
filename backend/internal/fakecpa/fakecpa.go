// Package fakecpa is a fake CLIProxyAPI instance (with the cpa-quota plugin)
// serving one Claude subscription account: /quota, /v1/models and the Claude
// Messages endpoints with usage (JSON or SSE) and the unified rate-limit
// response headers. Tests start it with New; cmd/fakecpa runs several of them
// standalone for local development (Drift mode).
package fakecpa

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// Reply forces the next relay response.
type Reply struct {
	Status int
	Body   string
	Header map[string]string
}

// Request is a recorded relay request.
type Request struct {
	Path   string
	Header http.Header
	Body   []byte
}

// Usage reported by the fake for each successful relay response.
type Usage struct {
	Input, Output, CacheRead, CacheWrite, Reasoning int64
}

// CPA is a fake instance.
type CPA struct {
	Server     *httptest.Server
	Key        string
	QuotaToken string
	Instance   string
	Sub        string // reported subscription; "claude" unless a test overrides it
	Tier       string
	AuthIndex  string
	Models     []string
	Usage      Usage

	mu       sync.Mutex
	replies  []Reply
	requests []Request
	// FiveUsed / WeekUsed / totals drive the quota response.
	FiveUsed, WeekUsed   float64
	FiveTotal, WeekTotal float64
	FiveReset, WeekReset time.Time
	State                string
	DatabaseStatus       string
	QuotaStatus          int

	// BaseURL is the address of a fake served without httptest (standalone).
	BaseURL string
	// Drift makes every successful /v1/messages call add its cost to the
	// reported usage, varies the usage a little, and rolls the windows over
	// at their reset (standalone demo mode).
	Drift bool
	// Delay is waited before the first byte of a relay response.
	Delay time.Duration
}

// New starts a fake CPA on an httptest server.
func New(instance string) *CPA {
	c := NewUnstarted(instance)
	c.Server = httptest.NewServer(c)
	return c
}

// NewUnstarted builds a fake CPA without a server; serve it as an
// http.Handler and set BaseURL.
func NewUnstarted(instance string) *CPA {
	now := time.Now().UTC()
	c := &CPA{Key: "cpa-key-" + instance, QuotaToken: "quota-" + instance, Instance: instance, Sub: "claude", Tier: "max5x",
		AuthIndex: "acct-" + instance, Models: []string{"claude-opus-4-8", "claude-sonnet-4.6"},
		Usage:    Usage{Input: 100, Output: 50, CacheRead: 1000, CacheWrite: 200},
		FiveUsed: 10, WeekUsed: 20, FiveTotal: 10, WeekTotal: 100, State: "estimated", DatabaseStatus: "ok",
		FiveReset: now.Add(3 * time.Hour).Truncate(time.Second), WeekReset: now.Add(4 * 24 * time.Hour).Truncate(time.Hour)}
	return c
}

// ServeHTTP serves the fake.
func (c *CPA) ServeHTTP(w http.ResponseWriter, r *http.Request) { c.serve(w, r) }

// URL is the base URL.
func (c *CPA) URL() string {
	if c.Server != nil {
		return c.Server.URL
	}
	return c.BaseURL
}

// Close stops the server.
func (c *CPA) Close() {
	if c.Server != nil {
		c.Server.Close()
	}
}

// Push queues forced replies for the next relay requests.
func (c *CPA) Push(r ...Reply) {
	c.mu.Lock()
	c.replies = append(c.replies, r...)
	c.mu.Unlock()
}

// Requests returns recorded relay requests.
func (c *CPA) Requests() []Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Request(nil), c.requests...)
}

// Set changes quota fields under the lock.
func (c *CPA) Set(f func(c *CPA)) {
	c.mu.Lock()
	f(c)
	c.mu.Unlock()
}

func (c *CPA) auth(r *http.Request, token string) bool {
	return r.Header.Get("Authorization") == "Bearer "+token
}

func (c *CPA) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/_fake/ratio_config":
		// Development only: a ratio table for the bridge's price_url.
		if !c.Drift {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, RatioTable)
	case "/_fake/quota-status":
		// Development control (standalone only): make /quota answer with the
		// given status, e.g. 503 to turn the account stale.
		if !c.Drift || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		code, _ := strconv.Atoi(r.URL.Query().Get("code"))
		c.Set(func(c *CPA) { c.QuotaStatus = code })
		w.WriteHeader(http.StatusNoContent)
	case "/v0/resource/plugins/cpa-quota/quota":
		if !c.auth(r, c.QuotaToken) {
			writeJSON(w, 401, map[string]any{"error": map[string]any{"code": "unauthorized", "message": "bad token"}})
			return
		}
		c.mu.Lock()
		c.rollover(time.Now().UTC())
		status := c.QuotaStatus
		body := c.quotaBody()
		c.mu.Unlock()
		if status != 0 && status != 200 {
			writeJSON(w, status, map[string]any{"error": "down"})
			return
		}
		writeJSON(w, 200, body)
	case "/v1/models":
		if !c.auth(r, c.Key) {
			writeJSON(w, 401, map[string]any{"error": "bad key"})
			return
		}
		data := []map[string]any{}
		for _, m := range c.Models {
			data = append(data, map[string]any{"id": m, "object": "model"})
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": data})
	default:
		c.relay(w, r)
	}
}

func (c *CPA) quotaBody() map[string]any {
	window := func(used, total float64, reset time.Time) map[string]any {
		return map[string]any{"used_percent": used, "reset_at": reset.Format(time.RFC3339), "total_usd": total,
			"remaining_usd": 0.9 * total * (1 - used/100)}
	}
	return map[string]any{
		"plugin": "cpa-quota", "instance": c.Instance, "subscription": c.Sub, "tier": c.Tier,
		"database":  map[string]any{"status": c.DatabaseStatus},
		"estimates": map[string]any{"loaded": true},
		"accounts": []map[string]any{{
			"auth_index": c.AuthIndex, "provider": "claude",
			"subscription": c.Sub, "tier": c.Tier, "state": c.State, "safety_factor": 0.9,
			"observed_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
			"five_hour":   window(c.FiveUsed, c.FiveTotal, c.FiveReset),
			"week":        window(c.WeekUsed, c.WeekTotal, c.WeekReset),
		}},
	}
}

// rollover resets a window whose reset has passed (Drift mode only). The
// caller holds the lock.
func (c *CPA) rollover(now time.Time) {
	if !c.Drift {
		return
	}
	for !c.FiveReset.After(now) {
		c.FiveReset = c.FiveReset.Add(5 * time.Hour)
		c.FiveUsed = 0
	}
	for !c.WeekReset.After(now) {
		c.WeekReset = c.WeekReset.Add(7 * 24 * time.Hour)
		c.WeekUsed = 0
	}
}

// cost is a rough upstream price of a usage (Opus list prices), in USD.
func (u Usage) cost() float64 {
	return (float64(u.Input)*15 + float64(u.Output)*75 + float64(u.CacheRead)*1.5 + float64(u.CacheWrite)*18.75) / 1e6
}

// rateLimitHeaders are the unified rate-limit headers Claude subscriptions
// report on every response. The caller holds the lock.
func (c *CPA) rateLimitHeaders(h http.Header) {
	status := "allowed"
	if c.FiveUsed >= 100 || c.WeekUsed >= 100 {
		status = "rejected"
	}
	h.Set("anthropic-ratelimit-unified-status", status)
	h.Set("anthropic-ratelimit-unified-5h-utilization", strconv.FormatFloat(min(c.FiveUsed, 100)/100, 'f', 4, 64))
	h.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(c.FiveReset.Unix(), 10))
	h.Set("anthropic-ratelimit-unified-7d-utilization", strconv.FormatFloat(min(c.WeekUsed, 100)/100, 'f', 4, 64))
	h.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(c.WeekReset.Unix(), 10))
}

// account records a successful call's usage (Drift mode). The caller holds
// the lock.
func (c *CPA) account(u Usage) {
	if !c.Drift {
		return
	}
	cost := u.cost()
	if c.FiveTotal > 0 {
		c.FiveUsed = min(100, c.FiveUsed+cost/c.FiveTotal*100)
	}
	if c.WeekTotal > 0 {
		c.WeekUsed = min(100, c.WeekUsed+cost/c.WeekTotal*100)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (c *CPA) relay(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.requests = append(c.requests, Request{Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	var forced *Reply
	if len(c.replies) > 0 {
		forced = &c.replies[0]
		c.replies = c.replies[1:]
	}
	u := c.Usage
	if c.Drift {
		// Vary the usage between 0.5× and 2× so the demo data is not flat.
		f := 0.5 + rand.Float64()*1.5 //nolint:gosec // demo data
		u = Usage{Input: int64(float64(u.Input) * f), Output: int64(float64(u.Output) * f),
			CacheRead: int64(float64(u.CacheRead) * f), CacheWrite: int64(float64(u.CacheWrite) * f)}
	}
	c.rollover(time.Now().UTC())
	if forced == nil && r.URL.Path == "/v1/messages" {
		c.account(u)
	}
	c.rateLimitHeaders(w.Header())
	delay := c.Delay
	c.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if !c.auth(r, c.Key) {
		writeJSON(w, 401, map[string]any{"error": map[string]any{"message": "invalid key"}})
		return
	}
	if forced != nil {
		for k, v := range forced.Header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(forced.Status)
		_, _ = io.WriteString(w, forced.Body)
		return
	}
	model := gjson.GetBytes(body, "model").String()
	stream := gjson.GetBytes(body, "stream").Bool()
	switch r.URL.Path {
	case "/v1/messages/count_tokens":
		writeJSON(w, 200, map[string]any{"input_tokens": 42})
	case "/v1/messages":
		usage := map[string]any{"input_tokens": u.Input, "cache_read_input_tokens": u.CacheRead,
			"cache_creation_input_tokens": u.CacheWrite, "output_tokens": u.Output}
		if !stream {
			writeJSON(w, 200, map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": model,
				"content": []map[string]any{{"type": "text", "text": "hi"}}, "stop_reason": "end_turn", "usage": usage})
			return
		}
		sse(w, []string{
			event("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_1", "type": "message",
				"role": "assistant", "model": model, "content": []any{}, "usage": map[string]any{"input_tokens": u.Input,
					"cache_read_input_tokens": u.CacheRead, "cache_creation_input_tokens": u.CacheWrite, "output_tokens": 1}}}),
			event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}),
			event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "hi"}}),
			event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
			event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"},
				"usage": map[string]any{"output_tokens": u.Output}}),
			event("message_stop", map[string]any{"type": "message_stop"}),
		})
	default:
		http.NotFound(w, r)
	}
}

func event(name string, v any) string {
	raw, _ := json.Marshal(v)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", name, raw)
}

func sse(w http.ResponseWriter, events []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	f, _ := w.(http.Flusher)
	for _, e := range events {
		_, _ = io.WriteString(w, e)
		if f != nil {
			f.Flush()
		}
	}
}

// RatioTable is the ratio table served at /_fake/ratio_config in Drift mode
// (the same shape as the price_url source; it covers the fake's models).
const RatioTable = `{"success":true,"data":{
	"model_ratio":{"claude-opus-4-8":2.5,"claude-sonnet-4.6":1.5},
	"completion_ratio":{"claude-opus-4-8":5,"claude-sonnet-4.6":5},
	"cache_ratio":{"claude-opus-4-8":0.1,"claude-sonnet-4.6":0.1},
	"create_cache_ratio":{"claude-opus-4-8":1.25,"claude-sonnet-4.6":1.25}}}`

// RefusalBody is the CPA safeguard error body.
func RefusalBody(category, reason string) string {
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{"type": "invalid_request_error",
		"message": "claude refusal (" + category + "): " + reason}})
	return string(raw)
}

// Contains reports whether any recorded request path contains s.
func (c *CPA) Contains(s string) bool {
	for _, r := range c.Requests() {
		if strings.Contains(r.Path, s) {
			return true
		}
	}
	return false
}
