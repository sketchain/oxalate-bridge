// Package usage reads token usage from CPA responses as the client sees them
// and converts it into the pricing package's breakdown.
//
// Token split by protocol (same semantics as cpa-quota's pricing.Split):
//   - Claude Messages: input, cache reads and cache writes are separate counts
//     and output already includes reasoning -> Split("claude").
//   - OpenAI Responses / Chat Completions: cache reads and writes are part of
//     the input / prompt count and reasoning is part of output -> Split("codex").
package usage

import (
	"bytes"

	"github.com/tidwall/gjson"

	"github.com/sketchain/oxalate-bridge/backend/internal/pricing"
)

// Protocol of an endpoint.
type Protocol int

// Protocols.
const (
	Messages Protocol = iota
	Responses
	Chat
)

// SplitProvider is the pricing.Split provider for a protocol.
func (p Protocol) SplitProvider() string {
	if p == Messages {
		return "claude"
	}
	return "codex"
}

// Usage accumulates what a response reported.
type Usage struct {
	Seen   bool
	Detail pricing.Detail
	// Model is the model the response names (served model), if any.
	Model string
}

// Breakdown splits the usage the way cpa-quota does for the protocol.
func (u Usage) Breakdown(p Protocol) pricing.Breakdown {
	if !u.Seen {
		return pricing.Breakdown{}
	}
	return pricing.Split(p.SplitProvider(), u.Detail)
}

func setIf(dst *int64, r gjson.Result) {
	if r.Exists() && r.Type == gjson.Number {
		*dst = r.Int()
	}
}

// mergeClaude merges a Claude usage object; later fields override, like
// CPA's claudeUsageTokens.Merge.
func (u *Usage) mergeClaude(usage gjson.Result) {
	if !usage.Exists() || !usage.IsObject() {
		return
	}
	u.Seen = true
	setIf(&u.Detail.InputTokens, usage.Get("input_tokens"))
	setIf(&u.Detail.OutputTokens, usage.Get("output_tokens"))
	setIf(&u.Detail.CacheCreationTokens, usage.Get("cache_creation_input_tokens"))
	setIf(&u.Detail.CacheReadTokens, usage.Get("cache_read_input_tokens"))
	setIf(&u.Detail.ReasoningTokens, usage.Get("output_tokens_details.thinking_tokens"))
}

func (u *Usage) setResponses(usage gjson.Result) {
	if !usage.Exists() || !usage.IsObject() {
		return
	}
	u.Seen = true
	d := pricing.Detail{}
	setIf(&d.InputTokens, usage.Get("input_tokens"))
	setIf(&d.OutputTokens, usage.Get("output_tokens"))
	setIf(&d.TotalTokens, usage.Get("total_tokens"))
	setIf(&d.CacheReadTokens, usage.Get("input_tokens_details.cached_tokens"))
	setIf(&d.CacheCreationTokens, usage.Get("input_tokens_details.cache_write_tokens"))
	if d.CacheCreationTokens == 0 {
		setIf(&d.CacheCreationTokens, usage.Get("input_tokens_details.cache_creation_tokens"))
	}
	setIf(&d.ReasoningTokens, usage.Get("output_tokens_details.reasoning_tokens"))
	u.Detail = d
}

func (u *Usage) setChat(usage gjson.Result) {
	if !usage.Exists() || !usage.IsObject() {
		return
	}
	u.Seen = true
	d := pricing.Detail{}
	setIf(&d.InputTokens, usage.Get("prompt_tokens"))
	setIf(&d.OutputTokens, usage.Get("completion_tokens"))
	setIf(&d.TotalTokens, usage.Get("total_tokens"))
	setIf(&d.CacheReadTokens, usage.Get("prompt_tokens_details.cached_tokens"))
	setIf(&d.CacheCreationTokens, usage.Get("prompt_tokens_details.cache_write_tokens"))
	if d.CacheCreationTokens == 0 {
		setIf(&d.CacheCreationTokens, usage.Get("prompt_tokens_details.cached_creation_tokens"))
	}
	setIf(&d.ReasoningTokens, usage.Get("completion_tokens_details.reasoning_tokens"))
	u.Detail = d
}

// FromBody reads the usage of a non-streaming response body.
func FromBody(p Protocol, body []byte) Usage {
	var u Usage
	if !gjson.ValidBytes(body) {
		return u
	}
	root := gjson.ParseBytes(body)
	u.Model = root.Get("model").String()
	switch p {
	case Messages:
		u.mergeClaude(root.Get("usage"))
	case Responses:
		u.setResponses(root.Get("usage"))
	case Chat:
		u.setChat(root.Get("usage"))
	}
	return u
}

// Event feeds one SSE event (its event name and joined data) and returns
// whether the event is a Chat usage-only chunk (empty choices plus usage),
// which the proxy drops when it injected include_usage itself.
func (u *Usage) Event(p Protocol, event string, data []byte) (usageOnlyChunk bool) {
	if len(data) == 0 || data[0] != '{' {
		return false
	}
	switch p {
	case Messages:
		typ := gjson.GetBytes(data, "type").String()
		if typ == "" {
			typ = event
		}
		switch typ {
		case "message_start":
			msg := gjson.GetBytes(data, "message")
			if m := msg.Get("model").String(); m != "" {
				u.Model = m
			}
			u.mergeClaude(msg.Get("usage"))
		case "message_delta":
			u.mergeClaude(gjson.GetBytes(data, "usage"))
		}
	case Responses:
		typ := gjson.GetBytes(data, "type").String()
		if typ == "" {
			typ = event
		}
		switch typ {
		case "response.completed", "response.incomplete", "response.failed", "response.done":
			resp := gjson.GetBytes(data, "response")
			if m := resp.Get("model").String(); m != "" {
				u.Model = m
			}
			u.setResponses(resp.Get("usage"))
		case "response.created":
			if m := gjson.GetBytes(data, "response.model").String(); m != "" {
				u.Model = m
			}
		}
	case Chat:
		root := gjson.ParseBytes(data)
		if m := root.Get("model").String(); m != "" && u.Model == "" {
			u.Model = m
		}
		us := root.Get("usage")
		if us.Exists() && us.IsObject() {
			u.setChat(us)
			choices := root.Get("choices")
			return !choices.Exists() || (choices.IsArray() && len(choices.Array()) == 0)
		}
	}
	return false
}

// SSEScanner splits an SSE byte stream into events without buffering more
// than the current event. Callers feed chunks as they pass them on.
type SSEScanner struct {
	line  []byte
	event string
	data  bytes.Buffer
	// OnEvent is called for each complete event with its raw bytes
	// (including the terminating blank line) when Raw is true.
	OnEvent func(event string, data []byte, raw []byte)
	Raw     bool
	raw     bytes.Buffer
}

// Feed processes a chunk.
func (s *SSEScanner) Feed(chunk []byte) {
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			s.line = append(s.line, chunk...)
			if s.Raw {
				s.raw.Write(chunk)
			}
			return
		}
		s.line = append(s.line, chunk[:i]...)
		if s.Raw {
			s.raw.Write(chunk[:i+1])
		}
		s.handleLine(bytes.TrimSuffix(s.line, []byte("\r")))
		s.line = s.line[:0]
		chunk = chunk[i+1:]
	}
}

// Flush dispatches a trailing event without a blank line.
func (s *SSEScanner) Flush() {
	if len(s.line) > 0 {
		s.handleLine(bytes.TrimSuffix(s.line, []byte("\r")))
		s.line = s.line[:0]
	}
	if s.data.Len() > 0 || s.event != "" || s.raw.Len() > 0 {
		s.dispatch()
	}
}

func (s *SSEScanner) handleLine(line []byte) {
	if len(line) == 0 {
		s.dispatch()
		return
	}
	if line[0] == ':' {
		return
	}
	field, value, _ := bytes.Cut(line, []byte(":"))
	value = bytes.TrimPrefix(value, []byte(" "))
	switch string(field) {
	case "event":
		s.event = string(value)
	case "data":
		if s.data.Len() > 0 {
			s.data.WriteByte('\n')
		}
		s.data.Write(value)
	}
}

func (s *SSEScanner) dispatch() {
	if s.OnEvent != nil {
		s.OnEvent(s.event, s.data.Bytes(), s.raw.Bytes())
	}
	s.event = ""
	s.data.Reset()
	s.raw.Reset()
}
