package fakecpa

import (
	"fmt"
	"time"
)

// DemoProfile shapes one demo account. It is the single source of the demo
// quota figures: cmd/fakecpa serves /quota from it and `bridge seed-demo`
// writes the demonstration cpa-quota estimates (tier, totals, safety factor,
// k) from the same profile, so the account detail page's runtime and
// estimate sections agree.
type DemoProfile struct {
	Tier                 string
	FiveUsed, WeekUsed   float64 // percent
	FiveTotal, WeekTotal float64 // USD per full window
	FiveReset, WeekReset time.Duration
	// K is the cpa-quota k (USD per percent point of the 5-hour window,
	// expressed as cpa-quota reports it) shown in the estimate.
	K float64
	// Safety is the plugin's safety factor (remaining = safety × total × (1 − used)).
	Safety float64
}

// DemoProfiles cover the states the panel shows: plenty of room, above the
// 5-hour watermark (penalized), a full 5-hour window, and a quiet account
// (seed-demo later makes the last one stale).
var DemoProfiles = []DemoProfile{
	{Tier: "max20x", FiveUsed: 18, WeekUsed: 32, FiveTotal: 40, WeekTotal: 400, FiveReset: 3 * time.Hour, WeekReset: 4 * 24 * time.Hour, K: 4.12, Safety: 0.9},
	{Tier: "max5x", FiveUsed: 78, WeekUsed: 55, FiveTotal: 10, WeekTotal: 100, FiveReset: 90 * time.Minute, WeekReset: 2 * 24 * time.Hour, K: 1.03, Safety: 0.9},
	{Tier: "max5x", FiveUsed: 100, WeekUsed: 71, FiveTotal: 10, WeekTotal: 100, FiveReset: 40 * time.Minute, WeekReset: 5 * 24 * time.Hour, K: 0.98, Safety: 0.9},
	{Tier: "pro", FiveUsed: 5, WeekUsed: 12, FiveTotal: 4, WeekTotal: 40, FiveReset: 4 * time.Hour, WeekReset: 6 * 24 * time.Hour, K: 0.41, Safety: 0.9},
}

// DemoProfileFor is the profile of demo instance i (1-based; profiles repeat).
func DemoProfileFor(i int) DemoProfile {
	return DemoProfiles[(max(i, 1)-1)%len(DemoProfiles)]
}

// DemoInstance is the name of demo instance i (1-based).
func DemoInstance(i int) string { return fmt.Sprintf("demo-%d", i) }

// ApplyDemo configures the fake from a demo profile, with resets relative to now.
func (c *CPA) ApplyDemo(p DemoProfile, now time.Time) {
	c.Set(func(c *CPA) {
		c.Tier, c.FiveUsed, c.WeekUsed, c.FiveTotal, c.WeekTotal = p.Tier, p.FiveUsed, p.WeekUsed, p.FiveTotal, p.WeekTotal
		c.FiveReset, c.WeekReset = now.Add(p.FiveReset).Truncate(time.Second), now.Add(p.WeekReset).Truncate(time.Minute)
		c.Safety = p.Safety
	})
}
