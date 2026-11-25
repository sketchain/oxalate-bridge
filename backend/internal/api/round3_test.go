package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/sketchain/oxalate-bridge/backend/internal/avail"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
)

func TestPlanFiguresUnavailableAccountIsZero(t *testing.T) {
	for _, status := range []string{avail.StatusDisabled, avail.StatusError} {
		acc := uuid.New()
		snap := &avail.Snapshot{ComputedAt: tnow, Accounts: []avail.Account{{ID: acc, Status: status,
			Five: avail.Window{Known: true, R: 5_000_000, T: 9_000_000, NextReset: tnow.Add(time.Hour)},
			Week: avail.Window{Known: true, R: 50_000_000, T: 90_000_000, NextReset: tnow.Add(48 * time.Hour)}}}}
		sub := sqlc.PlanSubscription{ID: uuid.New(), PlanName: "P", AccountID: &acc, WeeklyQuotaMicro: 10_000_000}
		period := PeriodUsage{PeriodStart: tnow.Add(-time.Hour), PeriodEnd: tnow.Add(24 * time.Hour), QuotaMicro: 10_000_000, RemainingMicro: 10_000_000}
		p := PlanFigures(snap, testSettings(), sub, period, tnow)
		if st, _ := p.AccountStatus.Get(); st != PlanAvailabilityAccountStatusUnavailable {
			t.Fatalf("%s: status %v", status, st)
		}
		five, _ := p.FiveHour.Get()
		per, _ := p.Period.Get()
		if five.AvailableMicro != 0 || per.AvailableMicro != 0 {
			t.Fatalf("%s: five %d period %d, want 0", status, five.AvailableMicro, per.AvailableMicro)
		}
	}
	// An account the snapshot does not know is unavailable too.
	missing := uuid.New()
	sub := sqlc.PlanSubscription{ID: uuid.New(), AccountID: &missing}
	p := PlanFigures(&avail.Snapshot{}, testSettings(), sub, PeriodUsage{PeriodEnd: tnow.Add(time.Hour), RemainingMicro: 1}, tnow)
	if st, _ := p.AccountStatus.Get(); st != PlanAvailabilityAccountStatusUnavailable {
		t.Fatalf("missing account: %v", st)
	}
	// The user-facing states are exactly four.
	for _, s := range []string{avail.StatusNormal, avail.StatusStale, avail.StatusCooldown} {
		if planAccountStatus(s) != PlanAvailabilityAccountStatus(s) {
			t.Fatalf("%s mapped to %s", s, planAccountStatus(s))
		}
	}
}

func TestUserViewsHideUpstreamFields(t *testing.T) {
	acc, key, sub := uuid.New(), uuid.New(), uuid.New()
	row := sqlc.RequestLog{RequestID: uuid.New(), CreatedAt: tnow, AccountID: &acc, KeyID: &key, SubscriptionID: &sub, Tier: "plan",
		UpstreamCostMicro: 123, BilledMicro: 456, TierMultiplier: 1.5, ModelMultiplier: 2, Retries: 1, SessionHash: "abc", ClientIp: "1.2.3.4"}
	raw, _ := json.Marshal(myLogOf(row))
	for _, leak := range []string{"upstream_cost_micro", "tier_multiplier", "model_multiplier", "account_id", "retries", "session_hash",
		"client_ip", acc.String(), `"abc"`} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("MyRequestLog leaks %q: %s", leak, raw)
		}
	}
	if !strings.Contains(string(raw), `"billed_micro":456`) {
		t.Fatalf("billed missing: %s", raw)
	}
	// The admin view keeps everything.
	adminRaw, _ := json.Marshal(logOf(row))
	for _, want := range []string{"upstream_cost_micro", "tier_multiplier", "account_id", "session_hash", "client_ip"} {
		if !strings.Contains(string(adminRaw), want) {
			t.Fatalf("RequestLog lacks %q: %s", want, adminRaw)
		}
	}
	s := Subscription{Id: uuid.New(), AccountId: nullable.NewNullableWithValue(acc), CurrentPeriod: nullable.NewNullableWithValue(
		PeriodUsage{QuotaMicro: 10, UsedBilledMicro: 3, UsedUpstreamMicro: 2, RemainingMicro: 7})}
	raw, _ = json.Marshal(mySubscriptionOf(s))
	for _, leak := range []string{"account", "used_upstream_micro", acc.String(), "user_id"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("MySubscription leaks %q: %s", leak, raw)
		}
	}
	if !strings.Contains(string(raw), `"remaining_micro":7`) {
		t.Fatalf("period missing: %s", raw)
	}
}

func TestDayBounds(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	// 2026-11-20 17:30 UTC is already 2026-11-21 01:30 in Shanghai.
	from, to := DayBounds(time.Date(2026, 11, 20, 17, 30, 0, 0, time.UTC), sh)
	if !from.Equal(time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2026, 11, 21, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("shanghai %v %v", from, to)
	}
	// Exactly local midnight belongs to the new day.
	from, _ = DayBounds(time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC), sh)
	if !from.Equal(time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("midnight %v", from)
	}
	// DST: 2026-03-08 in New York is 23 hours long.
	ny, _ := time.LoadLocation("America/New_York")
	from, to = DayBounds(time.Date(2026, 3, 8, 15, 0, 0, 0, time.UTC), ny)
	if to.Sub(from) != 23*time.Hour {
		t.Fatalf("dst day %v", to.Sub(from))
	}
}

func TestOverviewTiersOrderAndTotals(t *testing.T) {
	rows := []sqlc.RequestTotalsByTierRow{
		{Tier: "force", Requests: 1, BilledMicro: 30, UpstreamCostMicro: 10},
		{Tier: "", Requests: 2},
		{Tier: "plan", Requests: 5, BilledMicro: 100, UpstreamCostMicro: 90},
		{Tier: "standard", Requests: 3, BilledMicro: 40, UpstreamCostMicro: 35},
	}
	out, req, billed, upstream := OverviewTiers(rows)
	got := []string{}
	for _, r := range out {
		got = append(got, r.Tier)
	}
	if strings.Join(got, ",") != "plan,standard,force," {
		t.Fatalf("order %v", got)
	}
	if req != 11 || billed != 170 || upstream != 135 {
		t.Fatalf("totals %d %d %d", req, billed, upstream)
	}
}

func TestCountAccounts(t *testing.T) {
	mk := func(d AccountDisplayStatus, headroom, reserved int64) Account {
		return Account{Runtime: AccountRuntime{DisplayStatus: d, HeadroomMicro: headroom, ReservedMicro: reserved}}
	}
	out := CountAccounts([]Account{
		mk(AccountDisplayStatusNormal, 10, 1),
		mk(AccountDisplayStatusPenalized, 20, 2),
		mk(AccountDisplayStatusStale, 5, 0),
		mk(AccountDisplayStatusCooldown, -7, 3),
		mk(AccountDisplayStatusFiveHourFull, -3, 6),
		mk(AccountDisplayStatusWeekFull, 0, 0),
		mk(AccountDisplayStatusError, 100, 4),
		mk(AccountDisplayStatusDisabled, -100, 5),
	})
	// Negative headroom never offsets the others: 10 + 20 + 5 + 0, and the
	// two oversold accounts (−7, −3) are reported apart. Disabled / error
	// accounts count in neither.
	want := OverviewAccounts{Total: 8, Normal: 1, Penalized: 1, Full: 2, Stale: 1, Cooldown: 1, Error: 1, Disabled: 1,
		HeadroomMicro: 35, ReservedMicro: 21, OversoldCount: 2, OversoldMicro: 10}
	if out != want {
		t.Fatalf("got %+v want %+v", out, want)
	}
}

func TestDisplayPriority(t *testing.T) {
	cd := func(s string) *string { return &s }
	cases := []struct {
		name             string
		enabled, errored bool
		errReason        string
		cooldown         *string
		hard             string
		stale, penalized bool
		want             AccountDisplayStatus
		wantCode         string
	}{
		{"error beats disabled", false, true, "upstream 401: bad key", cd("network"), Reason5hExhausted, true, true, AccountDisplayStatusError, ReasonUpstreamAuth},
		{"other error", true, true, "boom", nil, "", false, false, AccountDisplayStatusError, ReasonError},
		{"disabled beats cooldown", false, false, "", cd("upstream 429"), "", true, false, AccountDisplayStatusDisabled, ReasonDisabled},
		{"cooldown 429", true, false, "", cd("upstream 429"), "cooldown", true, true, AccountDisplayStatusCooldown, ReasonUpstream429},
		{"cooldown 502", true, false, "", cd("upstream 502"), "cooldown", false, false, AccountDisplayStatusCooldown, ReasonUpstream5xx},
		{"cooldown network", true, false, "", cd("network"), "cooldown", false, false, AccountDisplayStatusCooldown, ReasonNetwork},
		{"cooldown error", true, false, "", cd("error: upstream 401"), "cooldown", false, false, AccountDisplayStatusCooldown, ReasonErrorCooldown},
		{"cooldown other", true, false, "", cd("odd"), "cooldown", false, false, AccountDisplayStatusCooldown, ReasonCooldown},
		{"5h full beats stale", true, false, "", nil, Reason5hExhausted, true, true, AccountDisplayStatusFiveHourFull, Reason5hExhausted},
		{"week full", true, false, "", nil, ReasonWeekExhausted, false, true, AccountDisplayStatusWeekFull, ReasonWeekExhausted},
		{"stale beats penalized", true, false, "", nil, "", true, true, AccountDisplayStatusStale, ReasonQuotaStale},
		{"penalized", true, false, "", nil, "", false, true, AccountDisplayStatusPenalized, ReasonPenalized},
		{"normal", true, false, "", nil, "", false, false, AccountDisplayStatusNormal, ""},
	}
	for _, c := range cases {
		d, code, _ := displayOf(c.enabled, c.errored, c.errReason, c.cooldown, c.hard, c.stale, "", c.penalized)
		if d != c.want || code != c.wantCode {
			t.Errorf("%s: got %s/%s want %s/%s", c.name, d, code, c.want, c.wantCode)
		}
		if code != "" && !slices.Contains(ReasonCodes, code) {
			t.Errorf("%s: code %s missing from ReasonCodes", c.name, code)
		}
	}
}

// Every reason code the backend produces has a zh-CN and an en translation
// in the panel (reason.<code>).
func TestReasonCodesTranslated(t *testing.T) {
	for _, locale := range []string{"zh-CN", "en"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "i18n", "locales", locale+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Reason map[string]string `json:"reason"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, code := range ReasonCodes {
			if strings.TrimSpace(doc.Reason[code]) == "" {
				t.Errorf("%s: reason.%s has no translation", locale, code)
			}
		}
	}
}
