package api

import (
	"context"
	"math"
	"slices"

	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/pricing"
	"github.com/sketchain/oxalate-bridge/backend/internal/settings"
)

// tokenPrices turns USD per 1M tokens into micro-USD per 1M tokens after the
// tier and model multipliers.
func tokenPrices(r pricing.Rates, tierMult, modelMult float64) *TokenPrices {
	m := func(usd float64) int64 { return int64(math.Round(usd * 1e6 * tierMult * modelMult)) }
	return &TokenPrices{Input: m(r.Input), Output: m(r.Output), CacheRead: m(r.CacheRead), CacheWrite: m(r.CacheWrite)}
}

// PricingOf lists every served model with its per-tier prices.
func PricingOf(models []string, t *pricing.Table, set settings.Values) Pricing {
	tm := set.TierMultipliers
	out := Pricing{TierMultipliers: TierMultipliers{Plan: tm.Plan, Standard: tm.Standard, Force: tm.Force}, Items: []ModelPricing{}}
	seen := map[string]bool{}
	for _, id := range models {
		norm := billing.NormalizeModel(id)
		if seen[norm] {
			continue
		}
		seen[norm] = true
		mm := set.ModelMultiplier(norm)
		item := ModelPricing{Model: norm, ModelMultiplier: mm}
		if rates, ok := t.Lookup(id, ""); ok {
			item.Priced = true
			item.Plan = tokenPrices(rates, tm.Plan, mm)
			item.Standard = tokenPrices(rates, tm.Standard, mm)
			item.Force = tokenPrices(rates, tm.Force, mm)
		}
		out.Items = append(out.Items, item)
	}
	slices.SortFunc(out.Items, func(a, b ModelPricing) int {
		if a.Model < b.Model {
			return -1
		}
		if a.Model > b.Model {
			return 1
		}
		return 0
	})
	return out
}

// GetPricing implements GET /pricing.
func (s *Server) GetPricing(ctx context.Context, _ GetPricingRequestObject) (GetPricingResponseObject, error) {
	return GetPricing200JSONResponse(PricingOf(s.Router.ServedModels(), s.Prices.Table(), s.Settings.Get())), nil
}
