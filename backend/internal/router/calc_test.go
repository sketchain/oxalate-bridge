package router

import (
	"math"
	"testing"
	"time"

	"github.com/sketchain/oxalate-bridge/backend/internal/cpa"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr[T any](v T) *T { return &v }

func TestCurrentPeriodAlignsWithWeekWindow(t *testing.T) {
	reset := ts("2026-10-10T04:00:00Z")
	now := ts("2026-10-07T12:00:00Z")
	p := CurrentPeriod(&reset, ts("2026-10-01T00:00:00Z"), now, nil)
	if p.Start != ts("2026-10-03T04:00:00Z") || p.End != reset {
		t.Fatalf("period %+v", p)
	}
	// Local roll-over once now passes reset_at, without a new poll.
	p = CurrentPeriod(&reset, time.Time{}, ts("2026-10-18T00:00:00Z"), nil)
	if p.Start != ts("2026-10-17T04:00:00Z") || p.End != ts("2026-10-24T04:00:00Z") {
		t.Fatalf("rolled period %+v", p)
	}
}

func TestCurrentPeriodEarlyResetStartsNewPeriod(t *testing.T) {
	prev := Period{Start: ts("2026-10-03T04:00:00Z"), End: ts("2026-10-10T04:00:00Z")}
	// Account reset early on the 7th: the plugin now reports reset_at = 14th 09:00.
	early := ts("2026-10-14T09:00:00Z")
	now := ts("2026-10-07T09:30:00Z")
	p := CurrentPeriod(&early, time.Time{}, now, &prev)
	if p.Start != ts("2026-10-07T09:00:00Z") || p.End != early {
		t.Fatalf("early reset period %+v", p)
	}
	if p == prev {
		t.Fatal("early reset kept the old period")
	}
	// Jitter within tolerance keeps the previous period.
	jitter := ts("2026-10-10T04:00:07Z")
	if got := CurrentPeriod(&jitter, time.Time{}, now, &prev); got != prev {
		t.Fatalf("jitter split the period: %+v", got)
	}
}

func TestCurrentPeriodWithoutWeekWindow(t *testing.T) {
	act := ts("2026-10-01T10:00:00Z")
	p := CurrentPeriod(nil, act, ts("2026-10-09T00:00:00Z"), nil)
	if p.Start != ts("2026-10-08T10:00:00Z") || p.End != ts("2026-10-15T10:00:00Z") {
		t.Fatalf("anchored period %+v", p)
	}
}

// A subscription active from mid-period to mid-period gets a full quota in
// the partial first and last windows: periods are keyed by window, not
// prorated, and the quota of each is the full weekly quota.
func TestPartialWindowsGetFullQuota(t *testing.T) {
	reset := ts("2026-10-10T04:00:00Z")
	activated := ts("2026-10-09T00:00:00Z") // one day before the window ends
	expires := ts("2026-10-17T12:00:00Z")   // ends mid-window
	first := CurrentPeriod(&reset, activated, activated, nil)
	last := CurrentPeriod(&reset, activated, expires.Add(-time.Minute), nil)
	if first.End != reset || last.Start != ts("2026-10-17T04:00:00Z") {
		t.Fatalf("first %+v last %+v", first, last)
	}
	if first == last {
		t.Fatal("first and last window collapsed")
	}
	// Reservation of an untouched period is the whole quota.
	if Reservation(10_000_000, 1) != 10_000_000 {
		t.Fatal("full quota not reserved")
	}
}

func TestReservationAndHeadroom(t *testing.T) {
	if Reservation(-5, 1) != 0 {
		t.Fatal("negative remaining reserved")
	}
	if Reservation(3_000_000, 1.5) != 2_000_000 {
		t.Fatal("plan multiplier not applied")
	}
	if Reservation(3_000_000, 0) != 3_000_000 || Reservation(3_000_000, -2) != 3_000_000 {
		t.Fatal("multiplier <= 0 must count as 1")
	}
	if h := Headroom(HeadroomInput{WeekRemainingMicro: 27_616_500, Spend: 1_000_000, Reserved: 20_000_000}); h != 6_616_500 {
		t.Fatalf("headroom %d", h)
	}
}

func snap(fiveUsed float64, weekUsed float64, hasFive bool) *cpa.Snapshot {
	reset5 := ts("2026-10-05T13:00:00Z")
	resetW := ts("2026-10-10T04:00:00Z")
	a := cpa.Account{State: "estimated", SafetyFactor: 0.9, ObservedAt: ptr(ts("2026-10-05T10:00:00Z")),
		Week: &cpa.Window{UsedPercent: weekUsed, ResetAt: &resetW, TotalUSD: 50, RemainingUSD: 0.9 * 50 * (1 - weekUsed/100)}}
	if hasFive {
		a.FiveHour = &cpa.Window{UsedPercent: fiveUsed, ResetAt: &reset5, TotalUSD: 8, RemainingUSD: 0.9 * 8 * (1 - fiveUsed/100)}
	}
	return &cpa.Snapshot{HasData: true, Account: a, FetchedAt: ts("2026-10-05T10:00:00Z")}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestWeightAndWatermark(t *testing.T) {
	now := ts("2026-10-05T11:00:00Z")
	if w := Weight(snap(40, 10, true), now, 70, 0.1); !near(w, 0.6) {
		t.Fatalf("below watermark %v", w)
	}
	if w := Weight(snap(80, 10, true), now, 70, 0.1); !near(w, 0.02) {
		t.Fatalf("above watermark %v", w)
	}
	// No 5h window: week remaining fraction, no watermark penalty.
	if w := Weight(snap(0, 80, false), now, 70, 0.1); !near(w, 0.2) {
		t.Fatalf("week-only %v", w)
	}
	// Local 5h reset: fresh window, full weight.
	if w := Weight(snap(95, 10, true), ts("2026-10-05T13:30:00Z"), 70, 0.1); !near(w, 1) {
		t.Fatalf("after local reset %v", w)
	}
}

func TestHardExhaustedAndLocalReset(t *testing.T) {
	now := ts("2026-10-05T11:00:00Z")
	s := snap(100, 10, true)
	if ex, why := cpa.HardExhausted(s.Account, now); !ex || why != "5h_exhausted" {
		t.Fatalf("5h full not exhausted: %v %s", ex, why)
	}
	if ex, _ := cpa.HardExhausted(s.Account, ts("2026-10-05T13:00:00Z")); ex {
		t.Fatal("window past reset_at still exhausted")
	}
	s.Account.State = "paused"
	if ex, _ := cpa.HardExhausted(s.Account, now); !ex {
		t.Fatal("paused not exhausted")
	}
	if ex, _ := cpa.HardExhausted(s.Account, ts("2026-10-05T13:01:00Z")); ex {
		t.Fatal("pause from a full window should end at its reset")
	}
	w, since, _ := cpa.EffectiveWindow(s.Account.FiveHour, 0.9, *s.Account.ObservedAt, ts("2026-10-05T14:00:00Z"))
	if w.UsedPercent != 0 || !near(w.RemainingUSD, 7.2) || since != ts("2026-10-05T13:00:00Z") {
		t.Fatalf("local reset window %+v since %v", w, since)
	}
}

func TestPickWeighted(t *testing.T) {
	weights := []float64{0.6, 0, 0.2}
	counts := make([]int, 3)
	for i := 0; i < 1000; i++ {
		counts[PickWeighted(weights, float64(i)/1000)]++
	}
	if counts[1] != 0 || counts[0] != 750 || counts[2] != 250 {
		t.Fatalf("counts %v", counts)
	}
	if PickWeighted([]float64{0, 0}, 0.75) != 1 {
		t.Fatal("all-zero weights must pick uniformly")
	}
	if PickWeighted(nil, 0.5) != -1 {
		t.Fatal("empty")
	}
}
