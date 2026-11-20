// Copied from github.com/sketchain/cpa-quota internal/pricing (MIT, same author)
// so the bridge prices requests exactly as the quota plugin does. Keep in sync.

// Package pricing turns a new-api ratio table into USD prices and prices the
// token usage CLIProxyAPI reports for Claude and Codex requests.
package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Rates are USD per 1,000,000 tokens.
type Rates struct {
	Input      float64 `json:"input_per_1m"`
	Output     float64 `json:"output_per_1m"`
	CacheRead  float64 `json:"cache_read_per_1m"`
	CacheWrite float64 `json:"cache_write_per_1m"`
}

// Table holds the rates of every model a ratio table prices, by model key.
type Table struct {
	rates map[string]Rates
}

// Len is the number of priced models.
func (t *Table) Len() int {
	if t == nil {
		return 0
	}
	return len(t.rates)
}

// ratioTable is the new-api ratio_config response.
type ratioTable struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		ModelRatio       map[string]float64 `json:"model_ratio"`
		CompletionRatio  map[string]float64 `json:"completion_ratio"`
		CacheRatio       map[string]float64 `json:"cache_ratio"`
		CreateCacheRatio map[string]float64 `json:"create_cache_ratio"`
	} `json:"data"`
}

// usdPerRatio is the USD per 1M input tokens of a model ratio of 1.
const usdPerRatio = 2

// Parse reads a ratio table:
//
//	input       = model_ratio × 2
//	output      = input × completion_ratio
//	cache read  = input × cache_ratio
//	cache write = input × create_cache_ratio
//
// A missing completion, cache or cache write ratio counts as 1, so that part
// is billed at the input rate. Models are keyed as ModelKey names them; a
// model without a valid model ratio is not priced.
func Parse(body []byte) (*Table, error) {
	var raw ratioTable
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode ratio table: %w", err)
	}
	if !raw.Success {
		if raw.Message != "" {
			return nil, fmt.Errorf("ratio table reports failure: %s", raw.Message)
		}
		return nil, errors.New("ratio table reports failure")
	}
	ratio := func(values map[string]float64, model string) float64 {
		if value, ok := values[model]; ok && valid(value) {
			return value
		}
		return 1
	}
	models := make([]string, 0, len(raw.Data.ModelRatio))
	for model := range raw.Data.ModelRatio {
		models = append(models, model)
	}
	sort.Strings(models)
	table := &Table{rates: map[string]Rates{}}
	for _, model := range models {
		modelRatio := raw.Data.ModelRatio[model]
		key := ModelKey(model)
		if key == "" || !valid(modelRatio) {
			continue
		}
		if _, taken := table.rates[key]; taken {
			// Spellings that share a key keep the first in name order.
			continue
		}
		input := modelRatio * usdPerRatio
		table.rates[key] = Rates{Input: input, Output: input * ratio(raw.Data.CompletionRatio, model),
			CacheRead: input * ratio(raw.Data.CacheRatio, model), CacheWrite: input * ratio(raw.Data.CreateCacheRatio, model)}
	}
	if len(table.rates) == 0 {
		return nil, errors.New("ratio table prices no model")
	}
	return table, nil
}

func valid(value float64) bool { return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) }

// WithoutOptions removes a trailing CLIProxyAPI request option such as
// "(high)" from a model name.
func WithoutOptions(model string) string {
	model = strings.TrimSpace(model)
	if open := strings.LastIndex(model, "("); open >= 0 && strings.HasSuffix(model, ")") {
		return strings.TrimSpace(model[:open])
	}
	return model
}

// ModelKey is the form models are looked up in: lower case, without request
// options and without a provider namespace before the last slash.
func ModelKey(model string) string {
	key := strings.ToLower(WithoutOptions(model))
	if i := strings.LastIndex(key, "/"); i >= 0 {
		key = key[i+1:]
	}
	return key
}

// BillingModel is the model a request is recorded under: the client-visible
// alias when one was used, else the upstream model, both without request
// options. An alias of "auto" names no model.
func BillingModel(upstream, alias string) string {
	upstream = WithoutOptions(upstream)
	base := WithoutOptions(alias)
	if base == "" || strings.EqualFold(base, "auto") || strings.EqualFold(base, upstream) {
		return upstream
	}
	return base
}

// lookupKeys are the keys a request's price is looked up by, in order: the
// upstream model, the alias, then either without a -high or -low suffix.
func lookupKeys(upstream, alias string) []string {
	var keys []string
	add := func(key string) {
		for _, existing := range keys {
			if existing == key {
				return
			}
		}
		if key != "" {
			keys = append(keys, key)
		}
	}
	add(ModelKey(upstream))
	if !strings.EqualFold(WithoutOptions(alias), "auto") {
		add(ModelKey(alias))
	}
	for _, key := range append([]string(nil), keys...) {
		for _, suffix := range []string{"-high", "-low"} {
			if base, found := strings.CutSuffix(key, suffix); found {
				add(base)
			}
		}
	}
	return keys
}

// Lookup finds a request's rates; false means the model is unpriced.
func (t *Table) Lookup(upstream, alias string) (Rates, bool) {
	if t == nil {
		return Rates{}, false
	}
	for _, key := range lookupKeys(upstream, alias) {
		if rates, ok := t.rates[key]; ok {
			return rates, true
		}
	}
	return Rates{}, false
}
