package router

import (
	"math"
	"time"

	"github.com/sketchain/oxalate-bridge/backend/internal/cpa"
)

// Week is the length of a plan period.
const Week = 7 * 24 * time.Hour

// periodTolerance absorbs jitter in reported reset times: a reset within it
// of the previous period's end continues that period.
const periodTolerance = 10 * time.Minute

// Period is one plan quota period [Start, End).
type Period struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Contains reports whether t falls inside the period.
func (p Period) Contains(t time.Time) bool { return !t.Before(p.Start) && t.Before(p.End) }

// CurrentPeriod returns the plan period containing now.
//
// Periods align with the bound account's week window: [reset_at − 7 d,
// reset_at). Once now passes reset_at the window rolls forward locally in
// 7-day steps. When the account resets early (reset_at moves), a new period
// starts at reset_at − 7 d of the new value. Without a week window the period
// is anchored at the subscription's activation in 7-day steps. prev, when
// given, is the period last used for the subscription: a reset_at within
// periodTolerance of prev.End keeps prev so jittery reports do not split it.
func CurrentPeriod(weekReset *time.Time, anchor time.Time, now time.Time, prev *Period) Period {
	if weekReset == nil || weekReset.IsZero() {
		if anchor.IsZero() || anchor.After(now) {
			anchor = now
		}
		n := now.Sub(anchor) / Week
		start := anchor.Add(n * Week).UTC()
		return Period{Start: start, End: start.Add(Week)}
	}
	end := weekReset.UTC().Truncate(time.Second)
	if !now.Before(end) {
		steps := now.Sub(end)/Week + 1
		end = end.Add(steps * Week)
	}
	if prev != nil {
		pe := prev.End
		for !now.Before(pe) {
			pe = pe.Add(Week)
		}
		if d := end.Sub(pe); d > -periodTolerance && d < periodTolerance {
			return Period{Start: pe.Add(-Week), End: pe}
		}
	}
	return Period{Start: end.Add(-Week), End: end}
}

// Reservation is how much of an account's week headroom a subscription still
// claims in upstream terms: max(0, remaining) / plan tier multiplier (a
// multiplier <= 0 counts as 1; model multipliers are ignored).
func Reservation(remaining int64, planMult float64) int64 {
	if remaining <= 0 {
		return 0
	}
	if planMult <= 0 {
		planMult = 1
	}
	return int64(math.Ceil(float64(remaining) / planMult))
}

// HeadroomInput is what H_A is computed from.
type HeadroomInput struct {
	WeekRemainingMicro int64 // effective week.remaining_usd in micro
	Spend              int64 // S_A: local upstream cost since observed_at (or local reset)
	Reserved           int64 // Σ reservations of active subscriptions
}

// Headroom is H_A = (week.remaining − S_A) − Σ reservations.
func Headroom(in HeadroomInput) int64 {
	return in.WeekRemainingMicro - in.Spend - in.Reserved
}

// Weight is an account's random-pick weight: the 5-hour remaining fraction,
// multiplied by penalty once 5-hour usage reaches the watermark. Claude
// accounts always report a 5-hour window; only when that data is missing
// does the week remaining fraction (without watermark) serve as a fallback.
func Weight(snap *cpa.Snapshot, now time.Time, watermark, penalty float64) float64 {
	if snap == nil || !snap.HasData {
		return 1
	}
	a := snap.Account
	observed := time.Time{}
	if a.ObservedAt != nil {
		observed = *a.ObservedAt
	}
	if w, _, ok := cpa.EffectiveWindow(a.FiveHour, a.SafetyFactor, observed, now); ok {
		weight := clamp01(1 - w.UsedPercent/100)
		if w.UsedPercent >= watermark {
			weight *= penalty
		}
		return weight
	}
	if w, _, ok := cpa.EffectiveWindow(a.Week, a.SafetyFactor, observed, now); ok {
		return clamp01(1 - w.UsedPercent/100)
	}
	return 1
}

func clamp01(v float64) float64 {
	switch {
	case math.IsNaN(v) || v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}

// PickWeighted returns the index chosen with probability proportional to
// weights, using r in [0, 1). When every weight is 0 the choice is uniform.
func PickWeighted(weights []float64, r float64) int {
	if len(weights) == 0 {
		return -1
	}
	total := 0.0
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	if total <= 0 {
		i := int(r * float64(len(weights)))
		return min(i, len(weights)-1)
	}
	target := r * total
	acc := 0.0
	last := -1
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		acc += w
		last = i
		if target < acc {
			return i
		}
	}
	return last
}
