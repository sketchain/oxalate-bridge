// Package usage reads token usage from CPA's Claude Messages responses as the
// client sees them and converts it into the pricing package's breakdown.
//
// Claude Messages reports input, cache reads and cache writes as separate
// counts and output already includes reasoning, so usage is split with
// cpa-quota's pricing.Split("claude").
package usage

import (
	"bytes"

	"github.com/tidwall/gjson"

	"github.com/sketchain/oxalate-bridge/backend/internal/pricing"
)

// splitProvider is the pricing.Split provider for Claude Messages.
const splitProvider = "claude"

// Usage accumulates what a response reported.
type Usage struct {
	Seen   bool
	Detail pricing.Detail
	// Model is the model the response names (served model), if any.
	Model string
}

// Breakdown splits the usage the way cpa-quota does for Claude.
func (u Usage) Breakdown() pricing.Breakdown {
	if !u.Seen {
		return pricing.Breakdown{}
	}
	return pricing.Split(splitProvider, u.Detail)
}

func setIf(dst *int64, r gjson.Result) {
	if r.Exists() && r.Type == gjson.Number {
		*dst = r.Int()
	}
}

// merge merges a Claude usage object; later fields override, like CPA's
// claudeUsageTokens.Merge.
func (u *Usage) merge(usage gjson.Result) {
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

// FromBody reads the usage of a non-streaming response body.
func FromBody(body []byte) Usage {
	var u Usage
	if !gjson.ValidBytes(body) {
		return u
	}
	root := gjson.ParseBytes(body)
	u.Model = root.Get("model").String()
	u.merge(root.Get("usage"))
	return u
}

// Event feeds one SSE event (its event name and joined data).
func (u *Usage) Event(event string, data []byte) {
	if len(data) == 0 || data[0] != '{' {
		return
	}
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
		u.merge(msg.Get("usage"))
	case "message_delta":
		u.merge(gjson.GetBytes(data, "usage"))
	}
}

// SSEScanner splits an SSE byte stream into events without buffering more
// than the current event. Callers feed chunks as they pass them on.
type SSEScanner struct {
	line  []byte
	event string
	data  bytes.Buffer
	// OnEvent is called for each complete event.
	OnEvent func(event string, data []byte)
}

// Feed processes a chunk.
func (s *SSEScanner) Feed(chunk []byte) {
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			s.line = append(s.line, chunk...)
			return
		}
		s.line = append(s.line, chunk[:i]...)
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
	if s.data.Len() > 0 || s.event != "" {
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
		s.OnEvent(s.event, s.data.Bytes())
	}
	s.event = ""
	s.data.Reset()
}
