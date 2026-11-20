package billing

import (
	"testing"

	"github.com/sketchain/oxalate-bridge/backend/internal/pricing"
	"github.com/sketchain/oxalate-bridge/backend/internal/usage"
)

// Same table as cpa-quota's pricing tests.
const sampleTable = `{"success":true,"data":{
	"model_ratio":{"claude-opus-4-8":2.5,"Claude-Sonnet-4.6":1.5,"gpt-5.6-sol":2,"vendor/bare-model":1,"bad":-1},
	"completion_ratio":{"claude-opus-4-8":5,"Claude-Sonnet-4.6":5,"gpt-5.6-sol":8},
	"cache_ratio":{"claude-opus-4-8":0.1,"gpt-5.6-sol":0.1},
	"create_cache_ratio":{"claude-opus-4-8":1.25}}}`

func table(t *testing.T) *pricing.Table {
	t.Helper()
	tb, err := pricing.Parse([]byte(sampleTable))
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestCostMatchesCPAQuota(t *testing.T) {
	tb := table(t)
	// cpa-quota TestCost: 1000 in, 2000 out (500 reasoning inside), 10000 cache read, 4000 cache write on opus.
	u := usage.FromBody(usage.Messages, []byte(`{"model":"claude-opus-4-8","usage":{"input_tokens":1000,"output_tokens":2000,
		"cache_read_input_tokens":10000,"cache_creation_input_tokens":4000,"output_tokens_details":{"thinking_tokens":500}}}`))
	micro, unpriced := Cost(tb, u.Model, "claude-opus-4-8", u.Breakdown(usage.Messages))
	want := USDToMicro((1000*5 + 2000*25 + 10000*0.5 + 4000*6.25) / 1e6)
	if unpriced || micro != want {
		t.Fatalf("cost = %d (%v), want %d", micro, unpriced, want)
	}
	// Responses: cache inside input, reasoning inside output (codex semantics).
	r := usage.FromBody(usage.Responses, []byte(`{"model":"gpt-5.6-sol","usage":{"input_tokens":128,"input_tokens_details":{"cached_tokens":32},
		"output_tokens":8,"output_tokens_details":{"reasoning_tokens":3},"total_tokens":136}}`))
	b := r.Breakdown(usage.Responses)
	if b != (pricing.Breakdown{Quality: pricing.QualityComplete, Uncached: 96, CacheRead: 32, Output: 8, Reasoning: 3, Total: 136}) {
		t.Fatalf("breakdown %+v", b)
	}
	micro, _ = Cost(tb, r.Model, "gpt-5.6-sol-high", b)
	if want := USDToMicro((96*4 + 32*0.4 + 8*32) / 1e6); micro != want {
		t.Fatalf("responses cost %d want %d", micro, want)
	}
	// Chat: prompt includes cache read and write.
	c := usage.FromBody(usage.Chat, []byte(`{"usage":{"prompt_tokens":1500,"completion_tokens":100,"total_tokens":1600,
		"prompt_tokens_details":{"cached_tokens":1000,"cache_write_tokens":400}}}`))
	b = c.Breakdown(usage.Chat)
	if b.Uncached != 100 || b.CacheRead != 1000 || b.CacheWrite != 400 || b.Output != 100 || !b.Billable() {
		t.Fatalf("chat breakdown %+v", b)
	}
	// Unpriced and inconsistent usage cost nothing.
	if m, unp := Cost(tb, "", "nope", b); m != 0 || !unp {
		t.Fatal("unpriced model priced")
	}
	bad := usage.FromBody(usage.Messages, []byte(`{"usage":{"input_tokens":-5,"output_tokens":3}}`))
	if m, _ := Cost(tb, "", "claude-opus-4-8", bad.Breakdown(usage.Messages)); m != 0 {
		t.Fatal("inconsistent usage priced")
	}
}

func TestEstimateAndApply(t *testing.T) {
	tb := table(t)
	// 3000 bytes -> 1000 tokens at $5/M + 4096 at $25/M.
	if got, want := Estimate(tb, "claude-opus-4-8", 3000, 4096), USDToMicro(1000*5/1e6+4096*25/1e6); got != want {
		t.Fatalf("estimate %d want %d", got, want)
	}
	if Estimate(tb, "unknown", 3000, 10) != 0 {
		t.Fatal("unpriced estimate")
	}
	if Apply(1000, 1.5, 2) != 3000 || Apply(333, 1, 0.5) != 167 {
		t.Fatal("apply")
	}
	if NormalizeModel("Anthropic/Claude-Opus-4-8(high)") != "claude-opus-4-8" {
		t.Fatal("normalize")
	}
}

func TestSSEUsage(t *testing.T) {
	var u usage.Usage
	sc := usage.SSEScanner{OnEvent: func(ev string, data, _ []byte) { u.Event(usage.Messages, ev, data) }}
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":5,\"output_tokens\":1}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\"}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":42}}\n\n"
	for i := 0; i < len(stream); i += 7 { // feed in odd-sized chunks
		end := min(i+7, len(stream))
		sc.Feed([]byte(stream[i:end]))
	}
	sc.Flush()
	if !u.Seen || u.Detail.InputTokens != 10 || u.Detail.CacheReadTokens != 5 || u.Detail.OutputTokens != 42 || u.Model != "claude-opus-4-8" {
		t.Fatalf("usage %+v", u)
	}
	var c usage.Usage
	if !c.Event(usage.Chat, "", []byte(`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)) {
		t.Fatal("usage-only chunk not detected")
	}
	if c.Event(usage.Chat, "", []byte(`{"choices":[{"delta":{}}]}`)) {
		t.Fatal("content chunk flagged")
	}
}
