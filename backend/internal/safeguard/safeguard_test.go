package safeguard

import (
	"strings"
	"testing"
)

func TestDetectError(t *testing.T) {
	cases := []struct {
		name, body, category, reason string
		ok                           bool
	}{
		{"cpa json", `{"type":"error","error":{"type":"invalid_request_error","message":"claude refusal (cyber): writing malware"}}`, "cyber", "writing malware", true},
		{"no category", "claude refusal: no category", "", "no category", true},
		{"bare", `{"message":"Claude Refusal"}`, "", "", true},
		{"category only", `{"error":"claude refusal (BIO)"}`, "bio", "", true},
		{"detail field", `{"detail":"claude refusal (frontier_llm): x"}`, "frontier_llm", "x", true},
		{"error payload with stop_details", `{"stop_reason":"end_turn","stop_details":{"type":"refusal","category":"general_harms","explanation":"no"}}`, "general_harms", "no", true},
		{"cyber policy text", `{"error":{"message":"This request triggered restrictions on violative cyber content and was blocked under Anthropic's Usage Policy."}}`, "cyber", "", true},
		{"ordinary 400", `{"error":{"message":"max_tokens too large"}}`, "", "", false},
		{"prefix inside", `{"error":{"message":"not a claude refusal"}}`, "", "", false},
		{"broken suffix", `claude refusal (cyber`, "", "", false},
	}
	for _, c := range cases {
		r, ok := DetectError([]byte(c.body))
		if ok != c.ok || r.Category != c.category || (c.reason != "" && r.Reason != c.reason) {
			t.Errorf("%s: got %+v %v", c.name, r, ok)
		}
	}
}

func TestDetectResponseAndEvent(t *testing.T) {
	if r, ok := DetectResponse([]byte(`{"type":"message","stop_reason":"refusal","content":[]}`)); !ok || r.Category != "" {
		t.Fatalf("stop_reason refusal: %+v %v", r, ok)
	}
	if _, ok := DetectResponse([]byte(`{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"a refusal is fine"}]}`)); ok {
		t.Fatal("ordinary answer mentioning refusal")
	}
	if _, ok := DetectResponse([]byte(`{"stop_details":{"type":"other","category":"weather"}}`)); ok {
		t.Fatal("unknown stop_details")
	}
	r, ok := DetectEvent("message_delta", []byte(`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"x"}},"usage":{"output_tokens":3}}`))
	if !ok || r.Category != "cyber" || r.Reason != "x" {
		t.Fatalf("message_delta: %+v %v", r, ok)
	}
	if _, ok := DetectEvent("", []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)); ok {
		t.Fatal("end_turn delta")
	}
	if r, ok := DetectEvent("error", []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"claude refusal (bio)"}}`)); !ok || r.Category != "bio" {
		t.Fatalf("error event: %+v %v", r, ok)
	}
	if _, ok := DetectEvent("content_block_delta", []byte(`{"delta":{"text":"claude refusal"}}`)); ok {
		t.Fatal("text delta detected")
	}
}

func TestPromptNormalization(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":"first"},
		{"role":"assistant","content":"ok"},
		{"role":"user","content":[
			{"type":"text","text":"<system-reminder>\ntoday is 2026-11-25\n</system-reminder>"},
			{"type":"text","text":"  Write   a\tport\nscanner  "},
			{"type":"image","source":{}}
		]}]}`
	if got := Prompt([]byte(body)); got != "Write a port scanner" {
		t.Fatalf("prompt %q", got)
	}
	// Same prompt, other reminder: same hash.
	other := strings.Replace(body, "2026-11-25", "2026-11-26", 1)
	if Hash(Prompt([]byte(body)), 0) != Hash(Prompt([]byte(other)), 0) {
		t.Fatal("system reminder changes the hash")
	}
	// Only tool results: their text.
	tool := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"result  text"}]},
		{"type":"text","text":"<system-reminder>x</system-reminder>"}]}]}`
	if got := Prompt([]byte(tool)); got != "result text" {
		t.Fatalf("tool prompt %q", got)
	}
	if got := Prompt([]byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","content":"plain"}]}]}`)); got != "plain" {
		t.Fatalf("string tool result %q", got)
	}
	if Prompt([]byte(`{"messages":[]}`)) != "" || Prompt([]byte(`not json`)) != "" {
		t.Fatal("empty prompt")
	}
	// Hash: sha256 hex, empty below the minimum length (in characters).
	if h := Hash("继续", 16); h != "" {
		t.Fatalf("short prompt hashed %q", h)
	}
	if h := Hash("abc", 0); h != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("hash %q", h)
	}
	if Truncate("一二三四", 2) != "一二" || Truncate("ab", 5) != "ab" {
		t.Fatal("truncate")
	}
}

func TestChunk(t *testing.T) {
	body := strings.Repeat("a", 1000) + strings.Repeat("中", 1000) // 1000 + 3000 bytes
	gz, err := Gzip([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	offset := int64(0)
	for n := 0; offset >= 0; n++ {
		if n > 10 {
			t.Fatal("too many chunks")
		}
		text, next, err := Chunk(gz, offset, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(body[offset:], text) || len(text) > 1024 {
			t.Fatalf("chunk at %d: %d bytes", offset, len(text))
		}
		got.WriteString(text)
		offset = next
	}
	if got.String() != body {
		t.Fatal("chunks do not add up")
	}
	if text, next, _ := Chunk(gz, int64(len(body)), 1024); text != "" || next != -1 {
		t.Fatalf("at end: %q %d", text, next)
	}
}

func TestAllowance(t *testing.T) {
	five := int32(5)
	if Allowance(nil, 3) != 3 || Allowance(&five, 3) != 5 {
		t.Fatal("allowance")
	}
	if Locked(2, nil, 3) || !Locked(3, nil, 3) || Locked(3, &five, 3) || !Locked(6, &five, 3) {
		t.Fatal("locked")
	}
}
