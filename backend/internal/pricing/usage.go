package pricing

import (
	"math"
	"strings"
)

// Detail is the token usage CLIProxyAPI reports in a usage record.
type Detail struct {
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
}

func (d Detail) empty() bool {
	return d.InputTokens == 0 && d.OutputTokens == 0 && d.ReasoningTokens == 0 && d.CachedTokens == 0 &&
		d.CacheReadTokens == 0 && d.CacheCreationTokens == 0 && d.TotalTokens == 0
}

// Accounting qualities of a breakdown. Only complete usage is priced:
// counts that contradict each other, or a total without its parts, are
// recorded but cost nothing rather than being guessed.
const (
	// QualityNone: the provider reported no usage.
	QualityNone         = ""
	QualityComplete     = "complete"
	QualityInconsistent = "inconsistent"
	QualityUnclassified = "unclassified"
)

// Breakdown is usage split into non-overlapping priced buckets. Output
// includes Reasoning.
type Breakdown struct {
	Quality    string
	Uncached   int64
	CacheRead  int64
	CacheWrite int64
	Output     int64
	Reasoning  int64
	Total      int64
}

// Billable reports whether the breakdown can be priced.
func (b Breakdown) Billable() bool { return b.Quality == QualityComplete }

// Split reads a usage record's counts with the semantics CLIProxyAPI's
// parser for the provider uses.
//
// Claude reports input, cache reads and cache writes as separate counts, and
// its output count already includes reasoning; when the reported total also
// adds reasoning, reasoning is separate from output. Codex (OpenAI style)
// reports cache reads and writes as part of input and reasoning as part of
// output.
func Split(provider string, detail Detail) Breakdown {
	if detail.empty() {
		return Breakdown{}
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return claudeSplit(detail)
	case "codex":
		return codexSplit(detail)
	}
	return unclassified(detail.TotalTokens)
}

func claudeSplit(d Detail) Breakdown {
	input, ok := sum(d.InputTokens, d.CacheReadTokens, d.CacheCreationTokens)
	if !ok || d.OutputTokens < 0 || d.ReasoningTokens < 0 {
		return inconsistent(d.TotalTokens, 0)
	}
	output := d.OutputTokens
	if separate, ok := sum(input, d.OutputTokens, d.ReasoningTokens); ok && d.TotalTokens == separate {
		output = d.OutputTokens + d.ReasoningTokens
	} else if !ok || d.ReasoningTokens > d.OutputTokens {
		return inconsistent(d.TotalTokens, 0)
	}
	expected, ok := sum(input, output)
	if !ok {
		return inconsistent(d.TotalTokens, 0)
	}
	total := d.TotalTokens
	switch {
	case total == 0:
		total = expected
	case total != expected:
		return inconsistent(d.TotalTokens, expected)
	}
	return Breakdown{Quality: QualityComplete, Uncached: d.InputTokens, CacheRead: d.CacheReadTokens, CacheWrite: d.CacheCreationTokens,
		Output: output, Reasoning: d.ReasoningTokens, Total: total}
}

func codexSplit(d Detail) Breakdown {
	input, output, reasoning := d.InputTokens, d.OutputTokens, d.ReasoningTokens
	cacheRead, cacheWrite, total := d.CacheReadTokens, d.CacheCreationTokens, d.TotalTokens
	if total > 0 && input == 0 && output == 0 && reasoning == 0 && cacheRead == 0 && cacheWrite == 0 && d.CachedTokens == 0 {
		return unclassified(total)
	}
	if total == 0 && input == 0 && output == 0 {
		// Parts without their parent counts cannot be priced.
		lower, ok := lowerBound(d)
		if !ok {
			return inconsistent(total, 0)
		}
		if lower > 0 {
			return unclassified(lower)
		}
	}
	// A part of a parent the provider did not report stays unclassified.
	if input == 0 {
		cacheRead, cacheWrite = 0, 0
	}
	if output == 0 {
		reasoning = 0
	}
	expected, okExpected := sum(input, output)
	cache, okCache := sum(cacheRead, cacheWrite)
	if !okExpected || !okCache || input < 0 || output < 0 || cache > input || reasoning < 0 || reasoning > output {
		return inconsistent(total, expected)
	}
	if total < 0 || total > 0 && total < expected {
		return inconsistent(total, expected)
	}
	breakdown := Breakdown{Quality: QualityComplete, Uncached: input - cache, CacheRead: cacheRead, CacheWrite: cacheWrite,
		Output: output, Reasoning: reasoning, Total: expected}
	if total > expected {
		if input > 0 && output > 0 {
			return inconsistent(total, expected)
		}
		// Tokens beyond the reported parts are unclassified.
		breakdown.Quality, breakdown.Total = QualityUnclassified, total
	}
	return breakdown
}

func lowerBound(d Detail) (int64, bool) {
	cache, ok := sum(d.CacheReadTokens, d.CacheCreationTokens)
	if !ok || d.InputTokens < 0 || d.OutputTokens < 0 || d.ReasoningTokens < 0 || d.CachedTokens < 0 {
		return 0, false
	}
	input := max(d.InputTokens, cache, d.CachedTokens)
	output := max(d.OutputTokens, d.ReasoningTokens)
	return sum(input, output)
}

func unclassified(total int64) Breakdown {
	switch {
	case total < 0:
		return inconsistent(total, 0)
	case total == 0:
		return Breakdown{Quality: QualityComplete}
	}
	return Breakdown{Quality: QualityUnclassified, Total: total}
}

func inconsistent(total, computed int64) Breakdown {
	if total <= 0 {
		total = computed
	}
	return Breakdown{Quality: QualityInconsistent, Total: max(total, 0)}
}

func sum(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || total > math.MaxInt64-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

// FastModeMultiplier is what a Codex priority request costs relative to a
// standard one when fast mode billing is on.
const FastModeMultiplier = 2.5

// Cost prices a breakdown in USD; usage that is not billable costs nothing.
func Cost(rates Rates, breakdown Breakdown) float64 {
	if !breakdown.Billable() {
		return 0
	}
	return perMillion(breakdown.Uncached, rates.Input) + perMillion(breakdown.CacheRead, rates.CacheRead) +
		perMillion(breakdown.CacheWrite, rates.CacheWrite) + perMillion(breakdown.Output, rates.Output)
}

func perMillion(tokens int64, rate float64) float64 {
	if tokens <= 0 || rate == 0 {
		return 0
	}
	return float64(tokens) / 1_000_000 * rate
}
