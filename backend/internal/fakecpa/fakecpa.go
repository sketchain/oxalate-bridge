// Package fakecpa is a fake CLIProxyAPI instance (with the cpa-quota plugin)
// for tests: /quota, /v1/models, and the relay endpoints with usage.
package fakecpa

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	Sub        string // claude | chatgpt
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
}

// New starts a fake CPA.
func New(instance string) *CPA {
	now := time.Now().UTC()
	c := &CPA{Key: "cpa-key-" + instance, QuotaToken: "quota-" + instance, Instance: instance, Sub: "claude", Tier: "max5x",
		AuthIndex: "acct-" + instance, Models: []string{"claude-opus-4-8", "claude-sonnet-4.6"},
		Usage:    Usage{Input: 100, Output: 50, CacheRead: 1000, CacheWrite: 200},
		FiveUsed: 10, WeekUsed: 20, FiveTotal: 10, WeekTotal: 100, State: "estimated", DatabaseStatus: "ok",
		FiveReset: now.Add(3 * time.Hour).Truncate(time.Second), WeekReset: now.Add(4 * 24 * time.Hour).Truncate(time.Hour)}
	c.Server = httptest.NewServer(http.HandlerFunc(c.serve))
	return c
}

// URL is the base URL.
func (c *CPA) URL() string { return c.Server.URL }

// Close stops the server.
func (c *CPA) Close() { c.Server.Close() }

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
	case "/v0/resource/plugins/cpa-quota/quota":
		if !c.auth(r, c.QuotaToken) {
			writeJSON(w, 401, map[string]any{"error": map[string]any{"code": "unauthorized", "message": "bad token"}})
			return
		}
		c.mu.Lock()
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
			"auth_index": c.AuthIndex, "provider": map[string]string{"claude": "claude", "chatgpt": "codex"}[c.Sub],
			"subscription": c.Sub, "tier": c.Tier, "state": c.State, "safety_factor": 0.9,
			"observed_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
			"five_hour":   window(c.FiveUsed, c.FiveTotal, c.FiveReset),
			"week":        window(c.WeekUsed, c.WeekTotal, c.WeekReset),
		}},
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
	c.mu.Unlock()
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
	case "/v1/responses":
		usage := map[string]any{"input_tokens": u.Input + u.CacheRead, "input_tokens_details": map[string]any{"cached_tokens": u.CacheRead},
			"output_tokens": u.Output, "output_tokens_details": map[string]any{"reasoning_tokens": u.Reasoning},
			"total_tokens": u.Input + u.CacheRead + u.Output}
		resp := map[string]any{"id": "resp_1", "object": "response", "model": model, "status": "completed", "usage": usage}
		if !stream {
			writeJSON(w, 200, resp)
			return
		}
		sse(w, []string{
			event("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_1", "model": model}}),
			event("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": "hi"}),
			event("response.completed", map[string]any{"type": "response.completed", "response": resp}),
		})
	case "/v1/chat/completions":
		usage := map[string]any{"prompt_tokens": u.Input + u.CacheRead, "completion_tokens": u.Output,
			"total_tokens": u.Input + u.CacheRead + u.Output, "prompt_tokens_details": map[string]any{"cached_tokens": u.CacheRead}}
		if !stream {
			writeJSON(w, 200, map[string]any{"id": "chat_1", "object": "chat.completion", "model": model,
				"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "hi"}, "finish_reason": "stop"}},
				"usage":   usage})
			return
		}
		chunks := []string{
			data(map[string]any{"id": "chat_1", "object": "chat.completion.chunk", "model": model, "choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": "hi"}}}}),
			data(map[string]any{"id": "chat_1", "object": "chat.completion.chunk", "model": model, "choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}),
		}
		if gjson.GetBytes(body, "stream_options.include_usage").Bool() {
			chunks = append(chunks, data(map[string]any{"id": "chat_1", "object": "chat.completion.chunk", "model": model, "choices": []any{}, "usage": usage}))
		}
		chunks = append(chunks, "data: [DONE]\n\n")
		sse(w, chunks)
	default:
		http.NotFound(w, r)
	}
}

func event(name string, v any) string {
	raw, _ := json.Marshal(v)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", name, raw)
}

func data(v any) string {
	raw, _ := json.Marshal(v)
	return "data: " + string(raw) + "\n\n"
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
