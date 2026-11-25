package keymeter

import (
	"strings"
	"testing"
	"time"
)

func TestRejectionsByScope(t *testing.T) {
	rec := time.Date(2026, 11, 22, 15, 4, 0, 0, time.UTC)
	cases := []struct {
		rej      *Rejection
		code     string
		tierOnly bool
		window   string
	}{
		{QuotaRejection("a"), CodeQuotaExhausted, false, ""},
		{WindowRejection("a", 1, &rec), CodeWindowExhausted, false, "1d"},
		{QuotaRejection("b"), CodeBalanceQuotaExhausted, true, ""},
		{WindowRejection("b", 0, &rec), CodeBalanceQuotaExhausted, true, "5h"},
		{WindowRejection("b", 2, nil), CodeBalanceQuotaExhausted, true, "7d"},
	}
	for i, c := range cases {
		if c.rej.Code != c.code || c.rej.TierOnly() != c.tierOnly || c.rej.Window != c.window || c.rej.Status != 429 || c.rej.Type != "rate_limit_error" {
			t.Errorf("case %d: %+v", i, c.rej)
		}
		if c.tierOnly != strings.Contains(c.rej.Text("en"), "plan tier is still available") || c.tierOnly != strings.Contains(c.rej.Text("zh-CN"), "Plan 档仍可用") {
			t.Errorf("case %d: message %q", i, c.rej.Message)
		}
	}
	if ex := WindowRejection("b", 0, &rec).Extra(); ex["window"] != "5h" || ex["recovers_at"] != "2026-11-22T15:04:00Z" {
		t.Errorf("extra %v", ex)
	}
	var none *Rejection
	if none.TierOnly() {
		t.Error("nil rejection is tier-only")
	}
}
