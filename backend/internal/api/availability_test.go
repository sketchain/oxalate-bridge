package api

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sketchain/oxalate-bridge/backend/internal/avail"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpaquota/cqsql"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/settings"
)

var tnow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func testSettings() settings.Values {
	v := settings.Defaults()
	v.TierMultipliers = settings.TierMultipliers{Plan: 2, Standard: 1.5, Force: 3}
	return v
}

func TestBalanceLimitsPoolFigures(t *testing.T) {
	snap := &avail.Snapshot{ComputedAt: tnow,
		Normal: map[string]int64{"1h": 1_000_000, "5h": 4_000_000, "24h": 10_000_000, "7d": 40_000_000},
		Force:  map[string]int64{"1h": 2_000_000, "5h": 5_000_000, "24h": 12_000_000, "7d": 50_000_000}}
	out := BuildAvailability(snap, testSettings(), 5_000_000, tnow)
	if !out.Estimated || len(out.Normal) != 4 || len(out.Force) != 4 || out.Normal[0].Horizon != "1h" || out.Normal[3].Horizon != "7d" {
		t.Fatalf("shape %+v", out)
	}
	// 1h normal: pool 1.5M at user price, below the 5M balance.
	if n := out.Normal[0]; n.PoolMicro != 1_500_000 || n.AvailableMicro != 1_500_000 || n.LimitedByBalance {
		t.Fatalf("normal 1h %+v", n)
	}
	// 5h normal: pool 6M > balance 5M -> limited by balance.
	if n := out.Normal[1]; n.PoolMicro != 6_000_000 || n.AvailableMicro != 5_000_000 || !n.LimitedByBalance {
		t.Fatalf("normal 5h %+v", n)
	}
	// Force uses its own multiplier.
	if f := out.Force[0]; f.PoolMicro != 6_000_000 || f.AvailableMicro != 5_000_000 || !f.LimitedByBalance {
		t.Fatalf("force 1h %+v", f)
	}
	// A negative balance shows 0 available.
	neg := BuildAvailability(snap, testSettings(), -10, tnow)
	if neg.Normal[0].AvailableMicro != 0 || !neg.Normal[0].LimitedByBalance {
		t.Fatalf("negative balance %+v", neg.Normal[0])
	}
}

func TestPlanFiguresShortPeriodAndMultipleSubscriptions(t *testing.T) {
	accA, accB := uuid.New(), uuid.New()
	snap := &avail.Snapshot{ComputedAt: tnow, Accounts: []avail.Account{
		{ID: accA, Status: avail.StatusNormal,
			Five: avail.Window{Known: true, R: 1_000_000, T: 9_000_000, NextReset: tnow.Add(2 * time.Hour)},
			Week: avail.Window{Known: true, R: 20_000_000, T: 90_000_000, NextReset: tnow.Add(36 * time.Hour)},
			// Reservations do not reduce a plan holder's own figures.
			Reserved: 50_000_000},
		{ID: accB, Status: avail.StatusStale,
			Week: avail.Window{Known: true, R: 3_000_000, T: 50_000_000, NextReset: tnow.Add(36 * time.Hour)}},
	}}
	set := testSettings()

	// Subscription 1 on A: the period ends in 36 h (less than 7 days).
	subA := sqlc.PlanSubscription{ID: uuid.New(), PlanName: "Max", AccountID: &accA, Status: "active"}
	end := tnow.Add(36 * time.Hour)
	expires := tnow.Add(30 * 24 * time.Hour)
	subA.ExpiresAt = &expires
	pA := PeriodUsage{PeriodStart: end.Add(-7 * 24 * time.Hour), PeriodEnd: end, QuotaMicro: 100_000_000,
		UsedBilledMicro: 40_000_000, RemainingMicro: 60_000_000}
	a := PlanFigures(snap, set, subA, pA, tnow)
	if st, _ := a.AccountStatus.Get(); st != "normal" {
		t.Fatalf("status %v", st)
	}
	five, _ := a.FiveHour.Get()
	if five.AvailableMicro != 2_000_000 { // min(60M, 1M × 2)
		t.Fatalf("five %+v", five)
	}
	if r, _ := five.ResetAt.Get(); !r.Equal(tnow.Add(2 * time.Hour)) {
		t.Fatalf("reset %v", r)
	}
	per, _ := a.Period.Get()
	// T_end = 36 h: the week reset sits exactly at the end and does not
	// count, so C7 = 20M; C5 = 1M + 7 × 9M = 64M. min = 20M, × 2 = 40M < 60M.
	if per.AvailableMicro != 40_000_000 {
		t.Fatalf("period %+v", per)
	}
	if e, _ := per.EndsAt.Get(); !e.Equal(end) {
		t.Fatalf("ends %v", e)
	}

	// Subscription 2 on B (no 5h window): five_hour is the remaining
	// quota; the period figure is limited by the remaining quota.
	subB := sqlc.PlanSubscription{ID: uuid.New(), PlanName: "Pro", AccountID: &accB, Status: "active"}
	pB := PeriodUsage{PeriodStart: tnow.Add(-24 * time.Hour), PeriodEnd: tnow.Add(6 * 24 * time.Hour), QuotaMicro: 5_000_000,
		UsedBilledMicro: 4_000_000, RemainingMicro: 1_000_000}
	b := PlanFigures(snap, set, subB, pB, tnow)
	fiveB, _ := b.FiveHour.Get()
	perB, _ := b.Period.Get()
	if st, _ := b.AccountStatus.Get(); st != "stale" || fiveB.AvailableMicro != 1_000_000 || !fiveB.ResetAt.IsNull() || perB.AvailableMicro != 1_000_000 {
		t.Fatalf("sub B %+v %+v %+v", b, fiveB, perB)
	}

	// The subscription expiring before the period end caps the horizon.
	short := tnow.Add(1 * time.Hour)
	subA.ExpiresAt = &short
	pA.RemainingMicro = 100_000_000
	c := PlanFigures(snap, set, subA, pA, tnow)
	perC, _ := c.Period.Get()
	if perC.AvailableMicro != 2_000_000 { // min(C5(1h)=1M, C7=20M) × 2
		t.Fatalf("expiry-capped period %+v", perC)
	}

	// An unknown (e.g. deleted) account shows as error with nothing usable.
	gone := uuid.New()
	subG := sqlc.PlanSubscription{ID: uuid.New(), PlanName: "Gone", AccountID: &gone, Status: "active"}
	g := PlanFigures(snap, set, subG, pB, tnow)
	fiveG, _ := g.FiveHour.Get()
	perG, _ := g.Period.Get()
	if st, _ := g.AccountStatus.Get(); st != "error" || fiveG.AvailableMicro != 0 || perG.AvailableMicro != 0 {
		t.Fatalf("unknown account %+v", g)
	}

	// Pending subscriptions carry only their status.
	p := pendingPlan(sqlc.PlanSubscription{ID: uuid.New(), PlanName: "Later", Status: "pending_assignment"})
	if p.Status != PlanAvailabilityStatusPendingAssignment || !p.FiveHour.IsNull() || !p.Period.IsNull() || !p.RemainingMicro.IsNull() || !p.AccountStatus.IsNull() {
		t.Fatalf("pending %+v", p)
	}
}

func TestMergeCostBuckets(t *testing.T) {
	h := func(n int) time.Time { return tnow.Truncate(time.Hour).Add(time.Duration(n) * time.Hour) }
	cpa := []cqRow{{h(0), 0.5, 2}, {h(2), 1.0, 1}}
	ours := []ourRow{{h(1), 300_000, 1}, {h(2), 900_000, 1}}
	got := MergeCostBuckets(toCQ(cpa), toOurs(ours))
	if len(got) != 3 || got[0].CpaQuotaCostMicro != 500_000 || got[0].BridgeUpstreamCostMicro != 0 ||
		got[1].CpaQuotaCostMicro != 0 || got[1].BridgeUpstreamCostMicro != 300_000 || got[1].DiffMicro != -300_000 ||
		got[2].CpaQuotaCostMicro != 1_000_000 || got[2].BridgeUpstreamCostMicro != 900_000 || got[2].DiffMicro != 100_000 {
		t.Fatalf("merge %+v", got)
	}
}

type cqRow struct {
	b   time.Time
	usd float64
	n   int64
}

type ourRow struct {
	b     time.Time
	micro int64
	n     int64
}

func toCQ(rows []cqRow) []cqsql.CostByBucketRow {
	out := make([]cqsql.CostByBucketRow, len(rows))
	for i, r := range rows {
		out[i] = cqsql.CostByBucketRow{Bucket: r.b, CostUsd: r.usd, Requests: r.n}
	}
	return out
}

func toOurs(rows []ourRow) []sqlc.AccountCostByBucketRow {
	out := make([]sqlc.AccountCostByBucketRow, len(rows))
	for i, r := range rows {
		out[i] = sqlc.AccountCostByBucketRow{Bucket: r.b, UpstreamCostMicro: r.micro, Requests: r.n}
	}
	return out
}
