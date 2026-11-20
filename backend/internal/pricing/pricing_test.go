package pricing

import (
	"math"
	"testing"
)

const sampleTable = `{"success":true,"data":{
	"model_ratio":{"claude-opus-4-8":2.5,"Claude-Sonnet-4.6":1.5,"gpt-5.6-sol":2,"vendor/bare-model":1,"bad":-1},
	"completion_ratio":{"claude-opus-4-8":5,"Claude-Sonnet-4.6":5,"gpt-5.6-sol":8},
	"cache_ratio":{"claude-opus-4-8":0.1,"gpt-5.6-sol":0.1},
	"create_cache_ratio":{"claude-opus-4-8":1.25},
	"image_ratio":{"gpt-image-2":1}}}`

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9*math.Max(1, math.Abs(want)) {
		t.Fatalf("%s = %.12g, want %.12g", name, got, want)
	}
}

func TestParseConvertsRatiosToUSDPerMillion(t *testing.T) {
	table, err := Parse([]byte(sampleTable))
	if err != nil {
		t.Fatal(err)
	}
	if table.Len() != 4 {
		t.Fatalf("priced models = %d", table.Len())
	}
	opus, ok := table.Lookup("claude-opus-4-8", "")
	if !ok {
		t.Fatal("opus is unpriced")
	}
	near(t, "opus input", opus.Input, 5)
	near(t, "opus output", opus.Output, 25)
	near(t, "opus cache read", opus.CacheRead, 0.5)
	near(t, "opus cache write", opus.CacheWrite, 6.25)
	// Missing cache ratios bill at the input rate, as missing cache prices did.
	sonnet, ok := table.Lookup("claude-sonnet-4.6", "")
	if !ok {
		t.Fatal("model keys ignore case")
	}
	near(t, "sonnet input", sonnet.Input, 3)
	near(t, "sonnet output", sonnet.Output, 15)
	near(t, "sonnet cache read", sonnet.CacheRead, 3)
	near(t, "sonnet cache write", sonnet.CacheWrite, 3)
	// A missing completion ratio bills output at the input rate.
	bare, ok := table.Lookup("bare-model", "")
	if !ok || bare.Output != bare.Input || bare.Input != 2 {
		t.Fatalf("bare model: %+v %v", bare, ok)
	}
	if _, ok := table.Lookup("bad", ""); ok {
		t.Fatal("negative model ratio priced")
	}
	for _, body := range []string{`{"success":false,"message":"nope"}`, `{"success":true,"data":{"model_ratio":{}}}`, `not json`} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatalf("parsed %s", body)
		}
	}
}

func TestLookupKeysAndBillingModel(t *testing.T) {
	table, err := Parse([]byte(sampleTable))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		upstream, alias string
		want            float64
		ok              bool
	}{
		{"claude-opus-4-8(high)", "", 5, true},
		{"anthropic/claude-opus-4-8", "", 5, true},
		{"internal-name", "claude-opus-4-8", 5, true},
		{"gpt-5.6-sol-high", "", 4, true},
		{"gpt-5.6-sol-low", "auto", 4, true},
		{"unknown", "auto", 0, false},
		{"unknown", "", 0, false},
	} {
		rates, ok := table.Lookup(test.upstream, test.alias)
		if ok != test.ok || rates.Input != test.want {
			t.Errorf("Lookup(%q, %q) = %+v, %v", test.upstream, test.alias, rates, ok)
		}
	}
	for _, test := range []struct{ upstream, alias, want string }{
		{"claude-opus-4-8(high)", "", "claude-opus-4-8"},
		{"gpt-5.6-sol", "auto", "gpt-5.6-sol"},
		{"gpt-5.6-sol", "my-alias(low)", "my-alias"},
		{"GPT-5.6-sol", "gpt-5.6-SOL", "GPT-5.6-sol"},
	} {
		if got := BillingModel(test.upstream, test.alias); got != test.want {
			t.Errorf("BillingModel(%q, %q) = %q", test.upstream, test.alias, got)
		}
	}
	var missing *Table
	if _, ok := missing.Lookup("claude-opus-4-8", ""); ok || missing.Len() != 0 {
		t.Fatal("a missing table prices nothing")
	}
}

func TestSplitFollowsProviderSemantics(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider string
		detail   Detail
		want     Breakdown
	}{
		{"claude reasoning inside output", "claude",
			Detail{InputTokens: 100, OutputTokens: 50, ReasoningTokens: 20, CacheReadTokens: 30, CacheCreationTokens: 10},
			Breakdown{Quality: QualityComplete, Uncached: 100, CacheRead: 30, CacheWrite: 10, Output: 50, Reasoning: 20, Total: 190}},
		{"claude total adds reasoning", "claude",
			Detail{InputTokens: 100, OutputTokens: 50, ReasoningTokens: 20, TotalTokens: 170},
			Breakdown{Quality: QualityComplete, Uncached: 100, Output: 70, Reasoning: 20, Total: 170}},
		{"claude total contradicts parts", "claude", Detail{InputTokens: 100, OutputTokens: 50, TotalTokens: 400},
			Breakdown{Quality: QualityInconsistent, Total: 400}},
		{"codex cache inside input", "codex",
			Detail{InputTokens: 128, OutputTokens: 8, ReasoningTokens: 3, CacheReadTokens: 32, TotalTokens: 136},
			Breakdown{Quality: QualityComplete, Uncached: 96, CacheRead: 32, Output: 8, Reasoning: 3, Total: 136}},
		{"codex cache beyond input", "codex", Detail{InputTokens: 10, OutputTokens: 8, CacheReadTokens: 32},
			Breakdown{Quality: QualityInconsistent, Total: 18}},
		{"codex total only", "codex", Detail{TotalTokens: 99}, Breakdown{Quality: QualityUnclassified, Total: 99}},
		{"codex parts without parents", "codex", Detail{CachedTokens: 40, ReasoningTokens: 5}, Breakdown{Quality: QualityUnclassified, Total: 45}},
		{"nothing reported", "claude", Detail{}, Breakdown{}},
	} {
		if got := Split(test.provider, test.detail); got != test.want {
			t.Errorf("%s: %+v, want %+v", test.name, got, test.want)
		}
	}
}

func TestCost(t *testing.T) {
	rates := Rates{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25}
	breakdown := Split("claude", Detail{InputTokens: 1000, OutputTokens: 2000, ReasoningTokens: 500, CacheReadTokens: 10000, CacheCreationTokens: 4000})
	// Reasoning is part of Claude's output and is charged once.
	near(t, "cost", Cost(rates, breakdown), (1000*5+2000*25+10000*0.5+4000*6.25)/1e6)
	if Cost(rates, Split("codex", Detail{TotalTokens: 5000})) != 0 {
		t.Fatal("unclassified usage was priced")
	}
}
