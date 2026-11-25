// Package safeguard is the bridge's protection against Claude policy
// refusals ("safeguard" triggers): it recognizes refusals in upstream
// responses, keeps each user's trigger allowance, reset cards and appeals,
// stores the request bodies of triggers for review, and maintains the
// site-wide blocklist of prompt hashes that are refused locally.
package safeguard

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// Refusal is what a refused response says.
type Refusal struct {
	// Category is the refusal category, such as cyber; "" when unnamed.
	Category string
	// Reason is the provider's explanation; "" when none was given.
	Reason string
}

// The recognition follows cpa-quota v0.1.0 (internal/safeguard), which
// mirrors CLIProxyAPI's clienterror.IsClaudePolicyRefusalError:
//
//   - the text CLIProxyAPI builds for a refused request, case-insensitively:
//     "claude refusal", "claude refusal (<category>)",
//     "claude refusal (<category>): <reason>" or "claude refusal: <reason>",
//     found in error.message (or message / error / detail) or as the body;
//   - a Messages payload, or a streaming message_delta, whose stop_details
//     describe a refusal (type refusal, or a refusal category), or whose
//     stop_reason is "refusal";
//   - Anthropic's plain-text cyber usage policy rejection (errors only).

// refusalCategories are the stop_details categories treated as refusals even
// without a refusal stop reason.
var refusalCategories = map[string]bool{"cyber": true, "bio": true, "frontier_llm": true, "reasoning_extraction": true,
	"general_harms": true}

const prefix = "claude refusal"

// DetectError reports whether an upstream error response body (status >= 400,
// or the data of an SSE error event) is a refusal.
func DetectError(body []byte) (Refusal, bool) {
	text := strings.TrimSpace(string(body))
	candidates := []string{}
	if gjson.Valid(text) {
		for _, path := range []string{"error.message", "message", "error", "detail"} {
			if r := gjson.Get(text, path); r.Type == gjson.String {
				candidates = append(candidates, strings.TrimSpace(r.String()))
			}
		}
	}
	candidates = append(candidates, text)
	for _, c := range candidates {
		if r, ok := fromText(c); ok {
			return r, true
		}
	}
	if r, ok := fromPayload([]byte(text)); ok {
		return r, true
	}
	for _, c := range candidates {
		lower := strings.ToLower(c)
		if strings.Contains(lower, "triggered restrictions on violative cyber content") && strings.Contains(lower, "anthropic's usage policy") {
			return Refusal{Category: "cyber", Reason: c}, true
		}
	}
	return Refusal{}, false
}

// DetectResponse reports whether a successful non-streaming Messages
// response stopped with a refusal.
func DetectResponse(body []byte) (Refusal, bool) {
	return fromPayload(body)
}

// DetectEvent reports whether one SSE event of a streaming response is a
// refusal: an error event carrying a refusal, or a message_delta that stops
// with one.
func DetectEvent(event string, data []byte) (Refusal, bool) {
	if event == "" {
		event = gjson.GetBytes(data, "type").String()
	}
	switch event {
	case "error":
		return DetectError(data)
	case "message_delta":
		return fromPayload(data)
	}
	return Refusal{}, false
}

// fromText parses the message CLIProxyAPI builds.
func fromText(text string) (Refusal, bool) {
	if len(text) < len(prefix) || !strings.EqualFold(text[:len(prefix)], prefix) {
		return Refusal{}, false
	}
	rest := text[len(prefix):]
	var r Refusal
	switch {
	case rest == "":
		return r, true
	case strings.HasPrefix(rest, " ("):
		end := strings.Index(rest, ")")
		if end < 0 {
			return Refusal{}, false
		}
		r.Category = strings.ToLower(strings.TrimSpace(rest[2:end]))
		rest = rest[end+1:]
		if rest == "" {
			return r, true
		}
		if !strings.HasPrefix(rest, ":") {
			return Refusal{}, false
		}
		r.Reason = strings.TrimSpace(rest[1:])
		return r, true
	case strings.HasPrefix(rest, ":"):
		r.Reason = strings.TrimSpace(rest[1:])
		return r, true
	}
	return Refusal{}, false
}

type stopDetails struct {
	Type        string `json:"type"`
	Category    string `json:"category"`
	Explanation string `json:"explanation"`
}

type stopPayload struct {
	StopReason  string          `json:"stop_reason"`
	StopDetails json.RawMessage `json:"stop_details"`
}

// fromPayload reads a Messages response, or a message_delta event, that
// stops with a refusal.
func fromPayload(raw []byte) (Refusal, bool) {
	s := string(raw)
	if !strings.Contains(s, "stop_details") && !strings.Contains(s, "refusal") {
		return Refusal{}, false
	}
	var payload struct {
		stopPayload
		Delta stopPayload `json:"delta"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return Refusal{}, false
	}
	for _, c := range []stopPayload{payload.Delta, payload.stopPayload} {
		var d stopDetails
		hasDetails := len(c.StopDetails) > 0 && string(c.StopDetails) != "null" && json.Unmarshal(c.StopDetails, &d) == nil
		category := strings.ToLower(strings.TrimSpace(d.Category))
		reasonRefusal := strings.EqualFold(strings.TrimSpace(c.StopReason), "refusal")
		if reasonRefusal || (hasDetails && (strings.EqualFold(strings.TrimSpace(d.Type), "refusal") || refusalCategories[category])) {
			return Refusal{Category: category, Reason: strings.TrimSpace(d.Explanation)}, true
		}
	}
	return Refusal{}, false
}
