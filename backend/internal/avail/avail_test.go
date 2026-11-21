package avail

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sketchain/oxalate-bridge/backend/internal/cpa"
)

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return now.Add(d) }

func TestResetsBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		next    time.Time
		period  time.Duration
		horizon time.Duration
		want    int64
	}{
		{"reset exactly at now+T does not count", at(time.Hour), Period5h, time.Hour, 0},
		{"reset after the horizon", at(2 * time.Hour), Period5h, time.Hour, 0},
		{"reset just inside", at(time.Hour - time.Second), Period5h, time.Hour, 1},
		{"first reset inside, next one exactly at now+T", at(time.Hour), Period5h, 6 * time.Hour, 2},
		{"first reset inside, next one just after", at(time.Hour), Period5h, 6*time.Hour - time.Second, 1},
		{"24h with resets every 5h from +1h", at(time.Hour), Period5h, 24 * time.Hour, 5},
		{"week reset in 3 days over 7 days", at(72 * time.Hour), Period7d, Period7d, 1},
		{"week reset exactly at now+7d", at(Period7d), Period7d, Period7d, 0},
		{"unknown reset", time.Time{}, Period5h, 24 * time.Hour, 0},
	}
	for _, c := range cases {
		if got := Resets(c.next, c.period, now, c.horizon); got != c.want {
			t.Errorf("%s: n = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCapacitiesAndForcedNormal(t *testing.T) {
	a := Account{Status: StatusNormal,
		Five: Window{Known: true, R: 2_000_000, T: 9_000_000, NextReset: at(2 * time.Hour)},
		Week: Window{Known: true, R: 30_000_000, T: 90_000_000, NextReset: at(72 * time.Hour)}}
	// 1h: no reset inside -> r only.
	if got := a.C5(now, time.Hour); got != 2_000_000 {
		t.Fatalf("C5(1h) %d", got)
	}
	// 5h: one 5h reset -> r5 + T5.
	if got := a.C5(now, 5*time.Hour); got != 11_000_000 {
		t.Fatalf("C5(5h) %d", got)
	}
	if got := a.Forced(now, 5*time.Hour); got != 11_000_000 {
		t.Fatalf("F(5h) %d", got)
	}
	// 7d: 5h resets 1 + floor((168-2)/5) = 34 -> C5 huge; week resets once -> r7 + T7.
	if got := a.Forced(now, Period7d); got != 120_000_000 {
		t.Fatalf("F(7d) %d", got)
	}
	// Normal subtracts the reservation once per week period touched.
	a.Reserved = 10_000_000
	if got := a.Normal(now, time.Hour); got != 2_000_000 { // min(2M, 30M-10M)
		t.Fatalf("S(1h) %d", got)
	}
	if got := a.Normal(now, Period7d); got != 100_000_000 { // min(C5, 120M - 2*10M)
		t.Fatalf("S(7d) %d", got)
	}
}

func TestReservationAboveCapacityIsZero(t *testing.T) {
	a := Account{Status: StatusNormal,
		Five: Window{Known: true, R: 5_000_000, T: 9_000_000, NextReset: at(time.Hour)},
		Week: Window{Known: true, R: 3_000_000, T: 90_000_000, NextReset: at(100 * time.Hour)},
		// More than the whole week remaining is reserved for plans.
		Reserved: 4_000_000}
	for _, h := range Horizons[:3] {
		if got := a.Normal(now, h.Duration); got != 0 {
			t.Fatalf("S(%s) = %d, want 0", h.Name, got)
		}
	}
	// Forced ignores reservations.
	if got := a.Forced(now, time.Hour); got != 3_000_000 {
		t.Fatalf("F(1h) %d", got)
	}
}

func TestNoFiveHourWindowIsUnlimited(t *testing.T) {
	a := Account{Status: StatusNormal, Week: Window{Known: true, R: 7_000_000, T: 50_000_000, NextReset: at(24 * time.Hour)}}
	if a.C5(now, time.Hour) != Infinite {
		t.Fatal("C5 should be infinite without a 5h window")
	}
	if got := a.Forced(now, time.Hour); got != 7_000_000 {
		t.Fatalf("F(1h) %d", got)
	}
	if got := a.Normal(now, 48*time.Hour); got != 57_000_000 {
		t.Fatalf("S(48h) %d", got)
	}
	// No week window either: capacity 0, not unlimited.
	if got := (Account{Status: StatusNormal}).Forced(now, Period7d); got != 0 {
		t.Fatalf("no windows: %d", got)
	}
}

func TestUnknownTotalMeansNoCapacity(t *testing.T) {
	w := WindowFrom(WindowInput{Window: &cpa.Window{UsedPercent: 10, ResetAt: ptr(at(time.Hour)), TotalUSD: 0, RemainingUSD: 0},
		Safety: 0.9, Observed: now, Period: Period5h}, now)
	a := Account{Status: StatusNormal, Five: w, Week: Window{Known: true, R: 10_000_000, T: 90_000_000}}
	if got := a.Forced(now, 24*time.Hour); got != 0 {
		t.Fatalf("unknown 5h total should give 0, got %d", got)
	}
}

func ptr[T any](v T) *T { return &v }

func TestWindowFromHardExhaustionAndLocalReset(t *testing.T) {
	five := &cpa.Window{UsedPercent: 40, ResetAt: ptr(at(2 * time.Hour)), TotalUSD: 10, RemainingUSD: 5.4}
	week := &cpa.Window{UsedPercent: 100, ResetAt: ptr(at(48 * time.Hour)), TotalUSD: 100, RemainingUSD: 0}
	observed := at(-time.Minute)

	// Normal case: r = remaining − spend, T = safety × total.
	w := WindowFrom(WindowInput{Window: five, Safety: 0.9, Observed: observed, Spend: 400_000, Period: Period5h}, now)
	if !w.Known || w.R != 5_000_000 || w.T != 9_000_000 || !w.NextReset.Equal(at(2*time.Hour)) {
		t.Fatalf("five %+v", w)
	}
	// Spend beyond remaining clamps at 0.
	if w := WindowFrom(WindowInput{Window: five, Safety: 0.9, Observed: observed, Spend: 9_000_000, Period: Period5h}, now); w.R != 0 {
		t.Fatalf("clamp %+v", w)
	}
	// used >= 100: r = 0 for that window, but future resets still count.
	wk := WindowFrom(WindowInput{Window: week, Safety: 0.9, Observed: observed, Period: Period7d}, now)
	a := Account{Status: StatusNormal, Five: w, Week: wk}
	if got := a.Forced(now, 24*time.Hour); got != 0 {
		t.Fatalf("week exhausted, F(24h) = %d", got)
	}
	if got := a.Forced(now, Period7d); got != min(a.C5(now, Period7d), 90_000_000) {
		t.Fatalf("week reset inside 7d should restore T7: %d", got)
	}
	// Paused / cooldown: both r are 0, resets still count.
	ex := WindowFrom(WindowInput{Window: five, Safety: 0.9, Observed: observed, Exhausted: true, Period: Period5h}, now)
	if ex.R != 0 || ex.T != 9_000_000 {
		t.Fatalf("exhausted %+v", ex)
	}
	b := Account{Status: StatusCooldown, Five: ex, Week: Window{Known: true, R: 0, T: 90_000_000, NextReset: at(Period7d)}}
	if got := b.C5(now, 3*time.Hour); got != 9_000_000 {
		t.Fatalf("cooldown C5(3h) %d", got)
	}
	// Local reset: a window whose reset_at passed counts as fresh, and its
	// next reset is one period later.
	past := &cpa.Window{UsedPercent: 95, ResetAt: ptr(at(-time.Hour)), TotalUSD: 10, RemainingUSD: 0.45}
	lr := WindowFrom(WindowInput{Window: past, Safety: 0.9, Observed: at(-2 * time.Hour), Spend: 1_000_000, Period: Period5h}, now)
	if lr.R != 8_000_000 || !lr.NextReset.Equal(at(4*time.Hour)) {
		t.Fatalf("local reset %+v", lr)
	}
}

func TestBuildParticipation(t *testing.T) {
	win := func(r int64) Window {
		return Window{Known: true, R: r, T: 100_000_000, NextReset: at(Period7d + time.Hour)}
	}
	mk := func(status string, r int64) Account {
		return Account{ID: uuid.New(), Status: status, Five: Window{}, Week: win(r)}
	}
	accounts := []Account{
		mk(StatusNormal, 1_000_000),
		mk(StatusStale, 2_000_000),    // forced only
		mk(StatusCooldown, 4_000_000), // forced only (its r would be 0 in practice)
		mk(StatusError, 8_000_000),    // neither
		mk(StatusDisabled, 16_000_000),
	}
	s := Build(accounts, now)
	if s.Normal["1h"] != 1_000_000 {
		t.Fatalf("normal %v", s.Normal)
	}
	if s.Force["1h"] != 7_000_000 {
		t.Fatalf("force %v", s.Force)
	}
	if len(s.Normal) != 4 || len(s.Force) != 4 {
		t.Fatal("all horizons expected")
	}
	if a, ok := s.Account(accounts[1].ID); !ok || a.Status != StatusStale {
		t.Fatal("lookup")
	}
}

func TestUserAmount(t *testing.T) {
	if UserAmount(1_000_000, 1.5) != 1_500_000 || UserAmount(1_000_000, 0) != 1_000_000 || UserAmount(Infinite, 2) != Infinite {
		t.Fatal("user amount")
	}
}
