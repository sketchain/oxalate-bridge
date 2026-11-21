package proxy

import (
	"context"
	"strings"

	"github.com/sketchain/oxalate-bridge/backend/internal/router"
)

// Admitter tries to admit a request on one tier. It returns a rejection when
// the tier does not admit the request; upstream errors are not rejections.
type Admitter[A any] func(ctx context.Context, tier string) (A, *router.Rejection)

// RunTiers tries tiers in order and returns the first admission, or every
// rejection when no tier admits the request.
func RunTiers[A any](ctx context.Context, tiers []string, admit Admitter[A]) (A, string, []router.Rejection) {
	var zero A
	var rejections []router.Rejection
	for _, tier := range tiers {
		a, rej := admit(ctx, tier)
		if rej == nil {
			return a, tier, rejections
		}
		rejections = append(rejections, *rej)
	}
	if len(tiers) == 0 {
		rejections = append(rejections, router.Rejection{Code: router.CodeUnsupportedTier, Message: "key 未开启任何档位"})
	}
	return zero, "", rejections
}

var tierLabels = map[string]string{router.TierPlan: "plan", router.TierStandard: "普通调用", router.TierForce: "强制调用"}

// RejectionMessage renders all rejections, tier by tier.
func RejectionMessage(rejections []router.Rejection) string {
	parts := make([]string, 0, len(rejections))
	for _, r := range rejections {
		label := tierLabels[r.Tier]
		if label == "" {
			label = r.Tier
		}
		if label == "" {
			parts = append(parts, r.Message)
			continue
		}
		parts = append(parts, label+": "+r.Message)
	}
	return "没有可用的档位（" + strings.Join(parts, "；") + "）"
}
