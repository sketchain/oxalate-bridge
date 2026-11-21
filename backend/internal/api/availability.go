package api

import (
	"context"
	"time"

	"github.com/oapi-codegen/nullable"

	"github.com/sketchain/oxalate-bridge/backend/internal/avail"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/settings"
)

// GetMyAvailability implements GET /me/availability. It combines the cached
// pool snapshot with the caller's balance, multipliers and subscriptions;
// it never names, counts or identifies upstream accounts.
func (s *Server) GetMyAvailability(ctx context.Context, _ GetMyAvailabilityRequestObject) (GetMyAvailabilityResponseObject, error) {
	snap, err := s.Avail.Get(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.Store.Q.GetUser(ctx, currentUser(ctx).ID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	_, balance, _, err := s.walletFigures(ctx, rstate.UserWallet(u.ID), u.BalanceMicro, u.BalanceVersion)
	if err != nil {
		return nil, err
	}
	subs, err := s.Store.Q.ListSubscriptionsByUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	set := s.Settings.Get()
	out := BuildAvailability(snap, set, balance, now)
	for _, sub := range subs {
		switch sub.Status {
		case "pending_assignment":
			out.Plans = append(out.Plans, pendingPlan(sub))
		case "active":
			view := s.subscriptionOf(ctx, sub)
			period, err := view.CurrentPeriod.Get()
			if err != nil {
				continue
			}
			out.Plans = append(out.Plans, PlanFigures(snap, set, sub, period, now))
		}
	}
	return GetMyAvailability200JSONResponse(out), nil
}

// BuildAvailability turns the pool snapshot into the caller's normal and
// forced figures (plans are added separately).
func BuildAvailability(snap *avail.Snapshot, set settings.Values, balance int64, now time.Time) Availability {
	out := Availability{ComputedAt: snap.ComputedAt.UTC(), Estimated: true, BalanceAvailableMicro: balance,
		Normal: []AvailabilityHorizon{}, Force: []AvailabilityHorizon{}, Plans: []PlanAvailability{}}
	for _, h := range avail.Horizons {
		out.Normal = append(out.Normal, horizonFigure(h.Name, snap.Normal[h.Name], set.TierMultipliers.Standard, balance))
		out.Force = append(out.Force, horizonFigure(h.Name, snap.Force[h.Name], set.TierMultipliers.Force, balance))
	}
	return out
}

func horizonFigure(name string, pool int64, mult float64, balance int64) AvailabilityHorizon {
	user := avail.UserAmount(pool, mult)
	return AvailabilityHorizon{Horizon: AvailabilityHorizonHorizon(name), PoolMicro: user,
		AvailableMicro: max(0, min(user, balance)), LimitedByBalance: balance < user}
}

func pendingPlan(sub sqlc.PlanSubscription) PlanAvailability {
	return PlanAvailability{SubscriptionId: sub.ID, PlanName: sub.PlanName, Status: PlanAvailabilityStatusPendingAssignment,
		AccountStatus: nullable.NewNullNullable[PlanAvailabilityAccountStatus](),
		PeriodStart:   nullable.NewNullNullable[time.Time](), PeriodEnd: nullable.NewNullNullable[time.Time](),
		QuotaMicro: nullable.NewNullNullable[int64](), UsedMicro: nullable.NewNullNullable[int64](),
		RemainingMicro: nullable.NewNullNullable[int64](), FiveHour: nullable.NewNullNullable[PlanWindowAvailability](),
		Period: nullable.NewNullNullable[PlanWindowAvailability]()}
}

// planAccountStatus maps an account status to what a plan holder sees:
// normal, stale or cooldown as they are; disabled, error or an account the
// snapshot does not know is unavailable.
func planAccountStatus(status string) PlanAvailabilityAccountStatus {
	switch status {
	case avail.StatusNormal, avail.StatusStale, avail.StatusCooldown:
		return PlanAvailabilityAccountStatus(status)
	}
	return PlanAvailabilityAccountStatusUnavailable
}

// PlanFigures computes one active subscription's figures:
//
//	five_hour = min(remaining, r5 × plan multiplier)
//	period    = min(remaining, min(C5, C7) until the period end × plan multiplier)
//
// The period figure never reaches into the next period (nor past the
// subscription's expiry when that comes first).
func PlanFigures(snap *avail.Snapshot, set settings.Values, sub sqlc.PlanSubscription, period PeriodUsage, now time.Time) PlanAvailability {
	remaining := max(0, period.RemainingMicro)
	out := PlanAvailability{SubscriptionId: sub.ID, PlanName: sub.PlanName, Status: PlanAvailabilityStatusActive,
		PeriodStart: nullable.NewNullableWithValue(period.PeriodStart), PeriodEnd: nullable.NewNullableWithValue(period.PeriodEnd),
		QuotaMicro: nullable.NewNullableWithValue(period.QuotaMicro), UsedMicro: nullable.NewNullableWithValue(period.UsedBilledMicro),
		RemainingMicro: nullable.NewNullableWithValue(period.RemainingMicro)}
	var acc avail.Account
	found := false
	if sub.AccountID != nil {
		acc, found = snap.Account(*sub.AccountID)
	}
	status := avail.StatusError
	if found {
		status = acc.Status
	}
	accStatus := planAccountStatus(status)
	out.AccountStatus = nullable.NewNullableWithValue(accStatus)
	// An unavailable account serves nothing: both figures are 0.
	usable := found && accStatus != PlanAvailabilityAccountStatusUnavailable
	mult := set.TierMultipliers.Plan

	five := PlanWindowAvailability{ResetAt: nullable.NewNullNullable[time.Time](), EndsAt: nullable.NewNullNullable[time.Time]()}
	switch {
	case !usable:
		five.AvailableMicro = 0
	case !acc.Five.Known:
		five.AvailableMicro = remaining // no 5-hour limit
	default:
		five.AvailableMicro = min(remaining, avail.UserAmount(acc.Five.R, mult))
		if !acc.Five.NextReset.IsZero() {
			five.ResetAt = nullable.NewNullableWithValue(avail.NextReset(acc.Five.NextReset, avail.Period5h, now).UTC())
		}
	}
	out.FiveHour = nullable.NewNullableWithValue(five)

	end := period.PeriodEnd
	if sub.ExpiresAt != nil && sub.ExpiresAt.Before(end) {
		end = *sub.ExpiresAt
	}
	horizon := max(0, end.Sub(now))
	per := PlanWindowAvailability{ResetAt: nullable.NewNullNullable[time.Time](), EndsAt: nullable.NewNullableWithValue(period.PeriodEnd.UTC())}
	if usable {
		per.AvailableMicro = min(remaining, avail.UserAmount(acc.Forced(now, horizon), mult))
	}
	out.Period = nullable.NewNullableWithValue(per)
	return out
}
