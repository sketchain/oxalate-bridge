package proxy

import (
	"context"
	"strings"

	"github.com/sketchain/oxalate-bridge/backend/internal/relaymsg"
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
		rejections = append(rejections, router.Rejection{Code: router.CodeUnsupportedTier, MsgID: relaymsg.NoTiersEnabled})
	}
	return zero, "", rejections
}

// LocalizeRejections renders every rejection's message in locale.
func LocalizeRejections(locale string, rejections []router.Rejection) {
	for i := range rejections {
		if rejections[i].MsgID != "" {
			rejections[i].Message = relaymsg.Text(locale, rejections[i].MsgID, rejections[i].MsgParams)
		}
	}
}

// RejectionMessage renders all (localized) rejections, tier by tier.
func RejectionMessage(locale string, rejections []router.Rejection) string {
	parts := make([]string, 0, len(rejections))
	for _, r := range rejections {
		label := relaymsg.TierLabel(locale, r.Tier)
		if label == "" {
			parts = append(parts, r.Message)
			continue
		}
		parts = append(parts, label+": "+r.Message)
	}
	return relaymsg.Text(locale, relaymsg.NoTier, map[string]string{"details": strings.Join(parts, relaymsg.Sep(locale))})
}
