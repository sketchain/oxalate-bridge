package proxy

import (
	"context"
	"strings"
	"testing"

	"github.com/sketchain/oxalate-bridge/backend/internal/relaymsg"
	"github.com/sketchain/oxalate-bridge/backend/internal/router"
)

func TestRunTiersOrderAndReasons(t *testing.T) {
	var tried []string
	admit := func(outcomes map[string]*router.Rejection) Admitter[string] {
		return func(_ context.Context, tier string) (string, *router.Rejection) {
			tried = append(tried, tier)
			if rej, ok := outcomes[tier]; ok && rej != nil {
				return "", rej
			}
			return "admitted:" + tier, nil
		}
	}
	planOut := &router.Rejection{Tier: "plan", Code: router.CodePlanExhausted, MsgID: relaymsg.PlanExhausted}
	stdOut := &router.Rejection{Tier: "standard", Code: router.CodeInsufficientFunds, MsgID: relaymsg.InsufficientFunds}

	// First admitting tier wins; later tiers are not tried.
	tried = nil
	a, tier, rejs := RunTiers(context.Background(), []string{"plan", "standard", "force"},
		admit(map[string]*router.Rejection{"plan": planOut}))
	if a != "admitted:standard" || tier != "standard" || len(rejs) != 1 || strings.Join(tried, ",") != "plan,standard" {
		t.Fatalf("got %q %q %v tried %v", a, tier, rejs, tried)
	}

	// The key's order is respected even when it starts with force.
	tried = nil
	_, tier, _ = RunTiers(context.Background(), []string{"force", "plan"}, admit(nil))
	if tier != "force" || strings.Join(tried, ",") != "force" {
		t.Fatalf("order not respected: %q %v", tier, tried)
	}

	// All rejected: every reason, in order.
	tried = nil
	_, tier, rejs = RunTiers(context.Background(), []string{"plan", "standard"},
		admit(map[string]*router.Rejection{"plan": planOut, "standard": stdOut}))
	if tier != "" || len(rejs) != 2 {
		t.Fatalf("expected two rejections: %q %v", tier, rejs)
	}
	zh := append([]router.Rejection(nil), rejs...)
	LocalizeRejections("zh-CN", zh)
	msg := RejectionMessage("zh-CN", zh)
	if !strings.Contains(msg, "plan: plan 本期额度已用完") || !strings.Contains(msg, "普通调用: 余额不足") ||
		strings.Index(msg, "plan:") > strings.Index(msg, "普通调用") || !strings.HasPrefix(msg, "没有可用的档位") {
		t.Fatalf("message %q", msg)
	}
	en := append([]router.Rejection(nil), rejs...)
	LocalizeRejections("en", en)
	msg = RejectionMessage("en", en)
	if msg != "no tier is available (plan: the plan's quota for this period is used up; standard: insufficient balance)" {
		t.Fatalf("en message %q", msg)
	}

	// No tiers at all.
	_, tier, rejs = RunTiers(context.Background(), nil, admit(nil))
	if tier != "" || len(rejs) != 1 || rejs[0].Code != router.CodeUnsupportedTier {
		t.Fatalf("empty tiers: %v", rejs)
	}
}

func TestHeaderFiltering(t *testing.T) {
	for _, h := range []string{"Authorization", "X-Api-Key", "x-goog-api-key", "Cookie", "X-Forwarded-For", "X-Forwarded-Host", "Connection", "Transfer-Encoding"} {
		if !clientHeaderDrop(h) {
			t.Errorf("%s forwarded", h)
		}
	}
	for _, h := range []string{"User-Agent", "Anthropic-Version", "Anthropic-Beta", "X-Claude-Code-Session-Id", "Session_id", "Content-Type"} {
		if clientHeaderDrop(h) {
			t.Errorf("%s dropped", h)
		}
	}
}
