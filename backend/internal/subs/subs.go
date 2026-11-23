// Package subs implements plan subscription operations shared by the panel
// API and the background jobs.
package subs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/router"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/settings"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
)

// Errors returned to the API layer.
var (
	ErrNotFound        = errors.New("not found")
	ErrNotOnSale       = errors.New("plan is not on sale")
	ErrInsufficient    = store.ErrInsufficientBalance
	ErrBadState        = errors.New("subscription state does not allow this")
	ErrAccountDisabled = errors.New("account is disabled or deleted")
)

// Service performs subscription operations.
type Service struct {
	Store    *store.Store
	St       *rstate.State
	Cat      *router.Catalog
	Settings *settings.Store
	Now      func() time.Time
	// OnPending runs after a purchase created a subscription waiting for
	// an account (notifications).
	OnPending func(ctx context.Context, sub sqlc.PlanSubscription)
	// OnRenewFailed runs after an automatic renewal failed.
	OnRenewFailed func(ctx context.Context, subID uuid.UUID, insufficient bool)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// AddDuration extends t by a plan duration.
func AddDuration(t time.Time, value int32, unit string) time.Time {
	switch unit {
	case "day":
		return t.AddDate(0, 0, int(value))
	case "week":
		return t.AddDate(0, 0, 7*int(value))
	default:
		return t.AddDate(0, int(value), 0)
	}
}

// availableBalance is the real-time available balance (Redis, synced from
// PG when missing).
func (s *Service) availableBalance(ctx context.Context, user uuid.UUID) (int64, error) {
	w := rstate.UserWallet(user)
	avail, err := s.St.Available(ctx, w, s.now())
	if !errors.Is(err, rstate.ErrWalletMissing) {
		return avail, err
	}
	row, err := s.Store.Q.GetUserBalance(ctx, user)
	if err != nil {
		return 0, err
	}
	if err := s.St.SyncBase(ctx, w, row.BalanceMicro, row.BalanceVersion); err != nil {
		return 0, err
	}
	return s.St.Available(ctx, w, s.now())
}

// Purchase buys a plan with the user's balance; the subscription waits for
// an admin to assign an account. Purchases never overdraw.
func (s *Service) Purchase(ctx context.Context, user, planID uuid.UUID, autoRenew bool) (sqlc.PlanSubscription, error) {
	plan, err := s.Store.Q.GetPlan(ctx, planID)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.PlanSubscription{}, ErrNotFound
	}
	if err != nil {
		return sqlc.PlanSubscription{}, err
	}
	if !plan.OnSale {
		return sqlc.PlanSubscription{}, ErrNotOnSale
	}
	avail, err := s.availableBalance(ctx, user)
	if err != nil {
		return sqlc.PlanSubscription{}, err
	}
	if avail < plan.PriceMicro {
		return sqlc.PlanSubscription{}, ErrInsufficient
	}
	var sub sqlc.PlanSubscription
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		id, _ := uuid.NewV7()
		sub, err = tx.Q.CreateSubscription(ctx, sqlc.CreateSubscriptionParams{ID: id, UserID: user, PlanID: plan.ID, PlanName: plan.Name,
			Subscription: plan.Subscription, WeeklyQuotaMicro: plan.WeeklyQuotaMicro, AllowedModels: plan.AllowedModels,
			DurationValue: plan.DurationValue, DurationUnit: plan.DurationUnit, PriceMicro: plan.PriceMicro,
			Status: "pending_assignment", AutoRenew: autoRenew})
		if err != nil {
			return err
		}
		zero := int64(0)
		if _, _, err := tx.AppendLedger(ctx, store.LedgerParams{UserID: user, Type: "plan_purchase", Amount: -plan.PriceMicro,
			RefType: "subscription", RefID: id.String(), IdempotencyKey: "plan_purchase:" + id.String(),
			Note: plan.Name, MinAfter: &zero, SyncRedis: true}); err != nil {
			return err
		}
		return tx.RoutingChanged(ctx)
	})
	if err == nil && s.OnPending != nil {
		go s.OnPending(context.WithoutCancel(ctx), sub)
	}
	return sub, err
}

// Grant opens a subscription for a user directly (admin), already active on
// an account, optionally charging the plan price. A dry run validates the
// same way and reports the same capacity and warnings, but writes nothing.
func (s *Service) Grant(ctx context.Context, operator, user, planID, accountID uuid.UUID, charge, dryRun bool) (sqlc.PlanSubscription, Check, error) {
	plan, err := s.Store.Q.GetPlan(ctx, planID)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.PlanSubscription{}, Check{}, ErrNotFound
	}
	if err != nil {
		return sqlc.PlanSubscription{}, Check{}, err
	}
	if _, err := s.checkAccount(ctx, accountID); err != nil {
		return sqlc.PlanSubscription{}, Check{}, err
	}
	now := s.now()
	expires := AddDuration(now, plan.DurationValue, plan.DurationUnit)
	var sub sqlc.PlanSubscription
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		if err := lockAccount(ctx, tx, accountID); err != nil {
			return err
		}
		id, _ := uuid.NewV7()
		sub, err = tx.Q.CreateSubscription(ctx, sqlc.CreateSubscriptionParams{ID: id, UserID: user, PlanID: plan.ID, PlanName: plan.Name,
			Subscription: plan.Subscription, WeeklyQuotaMicro: plan.WeeklyQuotaMicro, AllowedModels: plan.AllowedModels,
			DurationValue: plan.DurationValue, DurationUnit: plan.DurationUnit, PriceMicro: plan.PriceMicro,
			AccountID: &accountID, Status: "active", ActivatedAt: &now, ExpiresAt: &expires})
		if err != nil {
			return err
		}
		if charge && plan.PriceMicro != 0 {
			if _, _, err := tx.AppendLedger(ctx, store.LedgerParams{UserID: user, Type: "plan_purchase", Amount: -plan.PriceMicro,
				RefType: "subscription", RefID: id.String(), IdempotencyKey: "plan_purchase:" + id.String(),
				Note: "admin grant: " + plan.Name, OperatorID: &operator, SyncRedis: true}); err != nil {
				return err
			}
		}
		return finish(ctx, tx, dryRun)
	})
	return s.afterBind(ctx, sub, accountID, err)
}

// errDryRun rolls back a dry-run transaction.
var errDryRun = errors.New("dry run")

// finish ends a bind transaction: a dry run rolls back, a real one marks
// routing as changed.
func finish(ctx context.Context, tx *store.Tx, dryRun bool) error {
	if dryRun {
		return errDryRun
	}
	return tx.RoutingChanged(ctx)
}

// afterBind computes the capacity check of a bind (real or dry run).
func (s *Service) afterBind(ctx context.Context, sub sqlc.PlanSubscription, accountID uuid.UUID, err error) (sqlc.PlanSubscription, Check, error) {
	if err != nil && !errors.Is(err, errDryRun) {
		return sub, Check{}, err
	}
	chk, cerr := s.CheckCapacity(ctx, accountID, sub.ID, sub.WeeklyQuotaMicro)
	return sub, chk, cerr
}

// checkAccount loads an account a subscription is bound to. Accounts and
// plans are both Claude-only (database CHECK constraints), so there is no
// type to compare.
func (s *Service) checkAccount(ctx context.Context, accountID uuid.UUID) (sqlc.Account, error) {
	acc, err := s.Store.Q.GetAccount(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return acc, ErrNotFound
	}
	if err != nil {
		return acc, err
	}
	if !acc.Enabled {
		return acc, ErrAccountDisabled
	}
	return acc, nil
}

// lockAccount share-locks a live account for the rest of the transaction so
// a concurrent delete cannot slip between the delete guard and the bind.
func lockAccount(ctx context.Context, tx *store.Tx, accountID uuid.UUID) error {
	_, err := tx.Q.LockAccountForShare(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Warning codes of a bind (never blocking).
const (
	WarnOvercommit       = "overcommit"
	WarnWeekTotalUnknown = "week_total_unknown"
	WarnAccountNotNormal = "account_not_normal"
)

// Warning is one non-blocking remark about a bind.
type Warning struct {
	Code    string
	Message string
}

// Capacity is an account's sold capacity after a bind (the bound
// subscription included).
type Capacity struct {
	AccountID      uuid.UUID
	Quota          int64 // the bound subscription's weekly quota (user price)
	SoldWeekly     int64 // Σ weekly quota of active / pending subscriptions on the account
	SoldUpstream   int64 // SoldWeekly ÷ plan multiplier
	WeekTotal      *int64
	PlanMultiplier float64
}

// Check is the capacity and the warnings of a bind.
type Check struct {
	Capacity Capacity
	Warnings []Warning
}

// CheckCapacity computes the capacity of an account as it is with the
// subscription subID (weekly quota quota) bound to it, and the warnings:
// overcommit when the sold quotas in upstream terms exceed the account's
// week.total_usd, an unknown week total, and an account that is not in a
// normal state. Real binds and dry runs both use it, after their
// transaction, so their warnings are identical.
func (s *Service) CheckCapacity(ctx context.Context, accountID, subID uuid.UUID, quota int64) (Check, error) {
	others, err := s.Store.Q.SumQuotaByAccount(ctx, sqlc.SumQuotaByAccountParams{AccountID: &accountID, ID: subID})
	if err != nil {
		return Check{}, err
	}
	mult := s.Settings.Get().TierMultipliers.Plan
	if mult <= 0 {
		mult = 1
	}
	c := Capacity{AccountID: accountID, Quota: quota, SoldWeekly: others + quota, PlanMultiplier: mult}
	c.SoldUpstream = int64(float64(c.SoldWeekly) / mult)
	out := Check{Capacity: c, Warnings: []Warning{}}
	snap := s.Cat.Snapshot(accountID)
	if snap != nil && snap.HasData && snap.Account.Week != nil && snap.Account.Week.TotalUSD > 0 {
		total := billing.USDToMicro(snap.Account.Week.TotalUSD)
		out.Capacity.WeekTotal = &total
		if c.SoldUpstream > total {
			out.Warnings = append(out.Warnings, Warning{Code: WarnOvercommit, Message: fmt.Sprintf(
				"the weekly quotas sold on this account ($%.2f in upstream terms) exceed its week total $%.2f",
				float64(c.SoldUpstream)/1e6, float64(total)/1e6)})
		}
	} else {
		out.Warnings = append(out.Warnings, Warning{Code: WarnWeekTotalUnknown,
			Message: "the account has not reported a week total yet, so overcommit cannot be checked"})
	}
	if issue := s.accountIssue(ctx, accountID, snap); issue != "" {
		out.Warnings = append(out.Warnings, Warning{Code: WarnAccountNotNormal, Message: "the account is " + issue + " right now"})
	}
	return out, nil
}

// accountIssue names what keeps an account from serving now (error,
// cooldown, stale, full), or "".
func (s *Service) accountIssue(ctx context.Context, accountID uuid.UUID, snap *cpa.Snapshot) string {
	now := s.now()
	if acc, err := s.Store.Q.GetAccount(ctx, accountID); err == nil && acc.ErrorStatus {
		return "in error"
	}
	if cd, ok := s.St.Cooldown(ctx, accountID); ok && now.Before(cd.Until) {
		return "cooling down"
	}
	if snap.Stale(now, time.Duration(s.Settings.Get().StaleAfterSeconds)*time.Second) {
		return "stale"
	}
	if snap != nil && snap.HasData {
		if full, reason := cpa.HardExhausted(snap.Account, now); full {
			return "full (" + reason + ")"
		}
	}
	return ""
}

// Assign activates a pending subscription on an account (a dry run
// validates and reports without writing).
func (s *Service) Assign(ctx context.Context, subID, accountID uuid.UUID, dryRun bool) (sqlc.PlanSubscription, Check, error) {
	var out sqlc.PlanSubscription
	err := s.Store.InTx(ctx, func(tx *store.Tx) error {
		sub, err := tx.Q.GetSubscriptionForUpdate(ctx, subID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if sub.Status != "pending_assignment" {
			return ErrBadState
		}
		if _, err := s.checkAccount(ctx, accountID); err != nil {
			return err
		}
		if err := lockAccount(ctx, tx, accountID); err != nil {
			return err
		}
		now := s.now()
		expires := AddDuration(now, sub.DurationValue, sub.DurationUnit)
		out, err = tx.Q.UpdateSubscriptionState(ctx, sqlc.UpdateSubscriptionStateParams{ID: sub.ID, AccountID: &accountID,
			Status: "active", ActivatedAt: &now, ExpiresAt: &expires, AutoRenew: sub.AutoRenew})
		if err != nil {
			return err
		}
		return finish(ctx, tx, dryRun)
	})
	return s.afterBind(ctx, out, accountID, err)
}

// Rebind moves an active (or pending) subscription to another account (a
// dry run validates and reports without writing).
func (s *Service) Rebind(ctx context.Context, subID, accountID uuid.UUID, dryRun bool) (sqlc.PlanSubscription, Check, error) {
	var out sqlc.PlanSubscription
	err := s.Store.InTx(ctx, func(tx *store.Tx) error {
		sub, err := tx.Q.GetSubscriptionForUpdate(ctx, subID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if sub.Status != "active" && sub.Status != "pending_assignment" {
			return ErrBadState
		}
		if _, err := s.checkAccount(ctx, accountID); err != nil {
			return err
		}
		if err := lockAccount(ctx, tx, accountID); err != nil {
			return err
		}
		out, err = tx.Q.UpdateSubscriptionState(ctx, sqlc.UpdateSubscriptionStateParams{ID: sub.ID, AccountID: &accountID,
			Status: sub.Status, ActivatedAt: sub.ActivatedAt, ExpiresAt: sub.ExpiresAt, AutoRenew: sub.AutoRenew, CancelledAt: sub.CancelledAt})
		if err != nil {
			return err
		}
		return finish(ctx, tx, dryRun)
	})
	if err == nil {
		// Drop the stored period so the next request derives it from the
		// new account's week window.
		_ = s.St.R.Del(ctx, s.St.PeriodKey(subID)).Err()
	}
	return s.afterBind(ctx, out, accountID, err)
}

// Cancel cancels a subscription and refunds refund micro-USD (may be 0).
func (s *Service) Cancel(ctx context.Context, operator, subID uuid.UUID, refund int64, note string) (sqlc.PlanSubscription, error) {
	var out sqlc.PlanSubscription
	err := s.Store.InTx(ctx, func(tx *store.Tx) error {
		sub, err := tx.Q.GetSubscriptionForUpdate(ctx, subID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if sub.Status == "cancelled" {
			return ErrBadState
		}
		now := s.now()
		out, err = tx.Q.UpdateSubscriptionState(ctx, sqlc.UpdateSubscriptionStateParams{ID: sub.ID, AccountID: sub.AccountID,
			Status: "cancelled", ActivatedAt: sub.ActivatedAt, ExpiresAt: sub.ExpiresAt, AutoRenew: false, CancelledAt: &now})
		if err != nil {
			return err
		}
		if _, _, err := tx.AppendLedger(ctx, store.LedgerParams{UserID: sub.UserID, Type: "refund", Amount: refund,
			RefType: "subscription", RefID: sub.ID.String(), IdempotencyKey: "refund:" + sub.ID.String(), Note: note,
			OperatorID: &operator, SyncRedis: true}); err != nil {
			return err
		}
		return tx.RoutingChanged(ctx)
	})
	return out, err
}

// Renew extends a subscription by one duration, charging its price.
// The ledger idempotency key includes the previous expiry, so a renewal is
// applied once per period even if retried.
func (s *Service) Renew(ctx context.Context, user *uuid.UUID, subID uuid.UUID) (sqlc.PlanSubscription, error) {
	var out sqlc.PlanSubscription
	err := s.Store.InTx(ctx, func(tx *store.Tx) error {
		sub, err := tx.Q.GetSubscriptionForUpdate(ctx, subID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if user != nil && sub.UserID != *user {
			return ErrNotFound
		}
		if sub.Status != "active" && sub.Status != "expired" {
			return ErrBadState
		}
		now := s.now()
		base := now
		if sub.ExpiresAt != nil && sub.ExpiresAt.After(now) {
			base = *sub.ExpiresAt
		}
		expires := AddDuration(base, sub.DurationValue, sub.DurationUnit)
		prev := "none"
		if sub.ExpiresAt != nil {
			prev = sub.ExpiresAt.UTC().Format(time.RFC3339)
		}
		// A renewal must not overdraw the real-time balance.
		avail, err := s.availableBalance(ctx, sub.UserID)
		if err != nil {
			return err
		}
		if avail < sub.PriceMicro {
			return ErrInsufficient
		}
		zero := int64(0)
		_, inserted, err := tx.AppendLedger(ctx, store.LedgerParams{UserID: sub.UserID, Type: "plan_renewal", Amount: -sub.PriceMicro,
			RefType: "subscription", RefID: sub.ID.String(), IdempotencyKey: "plan_renewal:" + sub.ID.String() + ":" + prev,
			Note: sub.PlanName, MinAfter: &zero, SyncRedis: true})
		if err != nil {
			return err
		}
		if !inserted {
			out = sub
			return nil
		}
		activated := sub.ActivatedAt
		if activated == nil {
			activated = &now
		}
		out, err = tx.Q.UpdateSubscriptionState(ctx, sqlc.UpdateSubscriptionStateParams{ID: sub.ID, AccountID: sub.AccountID,
			Status: "active", ActivatedAt: activated, ExpiresAt: &expires, AutoRenew: sub.AutoRenew})
		if err != nil {
			return err
		}
		return tx.RoutingChanged(ctx)
	})
	return out, err
}

// SetAutoRenew toggles automatic renewal of a user's subscription.
func (s *Service) SetAutoRenew(ctx context.Context, user, subID uuid.UUID, on bool) (sqlc.PlanSubscription, error) {
	var out sqlc.PlanSubscription
	err := s.Store.InTx(ctx, func(tx *store.Tx) error {
		sub, err := tx.Q.GetSubscriptionForUpdate(ctx, subID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && sub.UserID != user) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if sub.Status == "cancelled" {
			return ErrBadState
		}
		out, err = tx.Q.UpdateSubscriptionState(ctx, sqlc.UpdateSubscriptionStateParams{ID: sub.ID, AccountID: sub.AccountID,
			Status: sub.Status, ActivatedAt: sub.ActivatedAt, ExpiresAt: sub.ExpiresAt, AutoRenew: on, CancelledAt: sub.CancelledAt})
		return err
	})
	return out, err
}

// ExpireDue marks active subscriptions past their expiry as expired.
func (s *Service) ExpireDue(ctx context.Context) (int, error) {
	ids, err := s.Store.Q.ListExpiredActive(ctx)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		err := s.Store.InTx(ctx, func(tx *store.Tx) error {
			sub, err := tx.Q.GetSubscriptionForUpdate(ctx, id)
			if err != nil {
				return err
			}
			if sub.Status != "active" || sub.ExpiresAt == nil || sub.ExpiresAt.After(s.now()) {
				return nil
			}
			if _, err := tx.Q.UpdateSubscriptionState(ctx, sqlc.UpdateSubscriptionStateParams{ID: sub.ID, AccountID: sub.AccountID,
				Status: "expired", ActivatedAt: sub.ActivatedAt, ExpiresAt: sub.ExpiresAt, AutoRenew: sub.AutoRenew}); err != nil {
				return err
			}
			n++
			return tx.RoutingChanged(ctx)
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// AutoRenewDue renews subscriptions with auto_renew that expire within the
// lead time. Failures (insufficient balance) leave them to expire.
func (s *Service) AutoRenewDue(ctx context.Context, lead time.Duration) (renewed int, failed int, err error) {
	due := s.now().Add(lead)
	ids, err := s.Store.Q.ListDueRenewals(ctx, &due)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		if _, err := s.Renew(ctx, nil, id); err != nil {
			// Insufficient balance: retried on later runs until it expires.
			failed++
			if s.OnRenewFailed != nil {
				s.OnRenewFailed(ctx, id, errors.Is(err, ErrInsufficient))
			}
			continue
		}
		renewed++
	}
	return renewed, failed, nil
}
