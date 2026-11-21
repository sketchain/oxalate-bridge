// Package avail estimates how much a user can still spend: the pooled
// capacity of the upstream accounts for normal (standard tier) and forced
// (force tier) calls over a few horizons, and the capacity behind each of a
// user's plans. All figures are estimates in upstream micro-USD; callers
// convert them to user prices with the tier multipliers.
//
// Per account A (local window resets applied as in routing):
//
//	T5 = safety × five_hour.total, T7 = safety × week.total (0 = unknown → capacity 0)
//	r5 = max(0, five_hour.remaining − S5), r7 = max(0, week.remaining − S7)
//	     (S = local upstream spend since the window's snapshot or local reset;
//	     r = 0 while hard exhausted: paused or in cooldown for both windows,
//	     used ≥ 100 % for that window)
//	n(T) = 0 if next_reset ≥ now+T, else ⌈(now+T − next_reset) / period⌉
//	       (only resets strictly before now+T count, for 5h and 7d alike)
//	C5(T) = r5 + n5(T)·T5 (∞ without a 5-hour window), C7(T) = r7 + n7(T)·T7
//	forced: F(T) = min(C5, C7)
//	normal: S(T) = max(0, min(C5, C7 − P·(1 + n7(T))))  (P = plan reservations)
//
// A 5-hour window is assumed to restart right after it resets, so n5 and the
// results are upper bounds.
package avail

import (
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpa"
)

// Infinite is the capacity of a dimension without a limit (no 5-hour window).
const Infinite int64 = math.MaxInt64

// Window periods.
const (
	Period5h = 5 * time.Hour
	Period7d = 7 * 24 * time.Hour
)

// Horizon is a time span the pool figures are given for.
type Horizon struct {
	Name     string
	Duration time.Duration
}

// Horizons are the spans of the pool figures, in display order.
var Horizons = []Horizon{
	{"1h", time.Hour},
	{"5h", 5 * time.Hour},
	{"24h", 24 * time.Hour},
	{"7d", 7 * 24 * time.Hour},
}

// Account statuses.
const (
	StatusNormal   = "normal"
	StatusStale    = "stale"
	StatusCooldown = "cooldown"
	StatusError    = "error"
	StatusDisabled = "disabled"
)

// Window is one quota window of an account.
type Window struct {
	// Known is false when the snapshot has no such window.
	Known bool `json:"known"`
	// R is the remaining capacity now (r), never negative.
	R int64 `json:"r"`
	// T is the capacity of a full window (safety × total); 0 when unknown.
	T int64 `json:"t"`
	// NextReset is the first reset after the computation time (zero when
	// the window reports none).
	NextReset time.Time `json:"next_reset,omitzero"`
}

// Resets is n(T): how many times a window with the given next reset and
// period resets strictly before now+horizon. With next ≥ now+T it is 0;
// otherwise ⌈(now+T − next) / period⌉, so a reset that lands exactly on
// now+T (the first one or any later one) does not count.
func Resets(next time.Time, period time.Duration, now time.Time, horizon time.Duration) int64 {
	if next.IsZero() || period <= 0 {
		return 0
	}
	end := now.Add(horizon)
	if !next.Before(end) {
		return 0
	}
	d := end.Sub(next)
	n := int64(d / period)
	if d%period != 0 {
		n++
	}
	return n
}

// capacity is r + n(T)·T, with T = 0 (unknown total) meaning no capacity.
func (w Window) capacity(period time.Duration, now time.Time, horizon time.Duration) int64 {
	if w.T <= 0 {
		return 0
	}
	return satAdd(w.R, satMul(Resets(w.NextReset, period, now, horizon), w.T))
}

// Account is the per-account input of the pool figures.
type Account struct {
	ID       uuid.UUID `json:"id"`
	Status   string    `json:"status"`
	Five     Window    `json:"five_hour"`
	Week     Window    `json:"week"`
	Reserved int64     `json:"reserved"` // P_A
}

// C5 is the 5-hour capacity over the horizon (Infinite without a window).
func (a Account) C5(now time.Time, h time.Duration) int64 {
	if !a.Five.Known {
		return Infinite
	}
	return a.Five.capacity(Period5h, now, h)
}

// C7 is the week capacity over the horizon (0 without a week window).
func (a Account) C7(now time.Time, h time.Duration) int64 {
	if !a.Week.Known {
		return 0
	}
	return a.Week.capacity(Period7d, now, h)
}

// Forced is F_A(T) = min(C5, C7).
func (a Account) Forced(now time.Time, h time.Duration) int64 {
	return max(0, min(a.C5(now, h), a.C7(now, h)))
}

// Normal is S_A(T) = max(0, min(C5, C7 − P·(1 + n7(T)))).
func (a Account) Normal(now time.Time, h time.Duration) int64 {
	n7 := Resets(a.Week.NextReset, Period7d, now, h)
	c7 := a.C7(now, h) - satMul(a.Reserved, 1+n7)
	return max(0, min(a.C5(now, h), c7))
}

// InForce reports whether the account counts for forced calls: enabled and
// not in error (stale accounts count with their last snapshot).
func (a Account) InForce() bool { return a.Status != StatusError && a.Status != StatusDisabled }

// InNormal reports whether the account counts for normal calls: enabled and
// not stale, in cooldown or in error.
func (a Account) InNormal() bool { return a.Status == StatusNormal }

// Snapshot is the cached, user-independent part of the availability.
type Snapshot struct {
	ComputedAt time.Time        `json:"computed_at"`
	Accounts   []Account        `json:"accounts"`
	Normal     map[string]int64 `json:"normal"`
	Force      map[string]int64 `json:"force"`
}

// Build sums the pools over the horizons.
func Build(accounts []Account, now time.Time) *Snapshot {
	s := &Snapshot{ComputedAt: now, Accounts: accounts, Normal: map[string]int64{}, Force: map[string]int64{}}
	for _, h := range Horizons {
		var normal, force int64
		for _, a := range accounts {
			if a.InForce() {
				force = satAdd(force, a.Forced(now, h.Duration))
			}
			if a.InNormal() {
				normal = satAdd(normal, a.Normal(now, h.Duration))
			}
		}
		s.Normal[h.Name], s.Force[h.Name] = normal, force
	}
	return s
}

// Account returns an account of the snapshot (there are only a handful).
func (s *Snapshot) Account(id uuid.UUID) (Account, bool) {
	for _, a := range s.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return Account{}, false
}

// WindowInput is what a Window is derived from.
type WindowInput struct {
	Window    *cpa.Window
	Safety    float64
	Observed  time.Time
	Spend     int64 // local spend since the window's snapshot or local reset
	Exhausted bool  // paused or in cooldown
	Period    time.Duration
}

// WindowFrom derives a Window, applying the local reset rule.
func WindowFrom(in WindowInput, now time.Time) Window {
	eff, _, ok := cpa.EffectiveWindow(in.Window, in.Safety, in.Observed, now)
	if !ok {
		return Window{}
	}
	out := Window{Known: true}
	if in.Window.TotalUSD > 0 {
		out.T = billing.USDToMicro(in.Safety * in.Window.TotalUSD)
	}
	if out.T > 0 && !in.Exhausted && eff.UsedPercent < 100 {
		out.R = max(0, billing.USDToMicro(eff.RemainingUSD)-in.Spend)
	}
	if in.Window.ResetAt != nil && !in.Window.ResetAt.IsZero() {
		out.NextReset = NextReset(*in.Window.ResetAt, in.Period, now)
	}
	return out
}

// NextReset rolls a reported reset forward in whole periods until it lies
// after now.
func NextReset(reset time.Time, period time.Duration, now time.Time) time.Time {
	if reset.After(now) || period <= 0 {
		return reset
	}
	steps := now.Sub(reset)/period + 1
	return reset.Add(steps * period)
}

// UserAmount converts an upstream capacity to the user price with a tier
// multiplier (model multipliers are ignored). Infinite stays Infinite.
func UserAmount(upstream int64, mult float64) int64 {
	if upstream == Infinite {
		return Infinite
	}
	if mult <= 0 {
		mult = 1
	}
	v := float64(upstream) * mult
	if v >= math.MaxInt64 {
		return Infinite
	}
	return billing.Apply(upstream, mult, 1)
}

func satAdd(a, b int64) int64 {
	if a == Infinite || b == Infinite || a > math.MaxInt64-b {
		return Infinite
	}
	return a + b
}

func satMul(n, v int64) int64 {
	if n == 0 || v == 0 {
		return 0
	}
	if v == Infinite || n > math.MaxInt64/v {
		return Infinite
	}
	return n * v
}
