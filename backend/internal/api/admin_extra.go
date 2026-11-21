package api

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"
	"github.com/redis/go-redis/v9"

	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/router"
	"github.com/sketchain/oxalate-bridge/backend/internal/settle"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
)

// ---- admin subscription views ----------------------------------------------------

// adminSubscriptions renders subscriptions with the owner's username and the
// bound account's name (one query for the usernames).
func (s *Server) adminSubscriptions(ctx context.Context, rows []sqlc.PlanSubscription) []Subscription {
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		if !slices.Contains(ids, r.UserID) {
			ids = append(ids, r.UserID)
		}
	}
	names := map[uuid.UUID]string{}
	if len(ids) > 0 {
		if found, err := s.Store.Q.ListUsernames(ctx, ids); err == nil {
			for _, u := range found {
				names[u.ID] = u.Username
			}
		} else {
			s.Log.Warn("list usernames", "err", err)
		}
	}
	out := make([]Subscription, len(rows))
	for i, r := range rows {
		sub := s.subscriptionOf(ctx, r)
		username := names[r.UserID]
		accountName := ""
		if r.AccountID != nil {
			if a, ok := s.Cat.Account(*r.AccountID); ok {
				accountName = a.Name
			}
		}
		sub.Username, sub.AccountName = &username, &accountName
		out[i] = sub
	}
	return out
}

// ---- account capacity figures --------------------------------------------------------

// fillSold sets the sold weekly quota of an account and its week total (the
// figures of the overcommit warning).
func (s *Server) fillSold(ctx context.Context, a *Account, st *router.AccountState) {
	id := a.Id
	sold, err := s.Store.Q.SumQuotaByAccount(ctx, sqlc.SumQuotaByAccountParams{AccountID: &id, ID: uuid.Nil})
	if err != nil {
		s.Log.Warn("sum sold quota", "account", id, "err", err)
	}
	a.SoldWeeklyQuotaMicro = sold
	mult := s.Settings.Get().TierMultipliers.Plan
	if mult <= 0 {
		mult = 1
	}
	a.SoldUpstreamMicro = int64(float64(sold) / mult)
	if st != nil && st.Snapshot != nil && st.Snapshot.HasData && st.Snapshot.Account.Week != nil && st.Snapshot.Account.Week.TotalUSD > 0 {
		a.WeekTotalMicro = nullable.NewNullableWithValue(billing.USDToMicro(st.Snapshot.Account.Week.TotalUSD))
	}
}

// ---- account validation (dry run) ------------------------------------------------------

// AdminValidateAccount implements POST /admin/accounts/validate.
func (s *Server) AdminValidateAccount(ctx context.Context, req AdminValidateAccountRequestObject) (AdminValidateAccountResponseObject, error) {
	if req.Body == nil || req.Body.BaseUrl == "" || req.Body.CpaKey == "" || req.Body.QuotaToken == "" {
		return nil, badRequest("base_url, cpa_key and quota_token are required")
	}
	v := s.validateAccount(ctx, req.Body.BaseUrl, req.Body.CpaKey, req.Body.QuotaToken)
	return AdminValidateAccount200JSONResponse(v.result), nil
}

// ---- user keys ---------------------------------------------------------------------------

// AdminListUserKeys implements GET /admin/users/{userId}/keys.
func (s *Server) AdminListUserKeys(ctx context.Context, req AdminListUserKeysRequestObject) (AdminListUserKeysResponseObject, error) {
	if _, err := s.Store.Q.GetUser(ctx, req.UserId); err != nil {
		return nil, mapDomainErr(err)
	}
	rows, err := s.Store.Q.ListAPIKeysByUser(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	items := make([]ApiKey, len(rows))
	for i, k := range rows {
		items[i] = keyOf(k)
	}
	return AdminListUserKeys200JSONResponse{Items: items}, nil
}

// AdminSetKeyEnabled implements PUT /admin/keys/{keyId}/enabled.
func (s *Server) AdminSetKeyEnabled(ctx context.Context, req AdminSetKeyEnabledRequestObject) (AdminSetKeyEnabledResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	k, err := s.Store.Q.GetAPIKey(ctx, req.KeyId)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound("key")
	}
	if err != nil {
		return nil, err
	}
	var row sqlc.ApiKey
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		row, err = tx.Q.UpdateAPIKey(ctx, sqlc.UpdateAPIKeyParams{ID: k.ID, Name: k.Name, Tiers: k.Tiers, SubscriptionID: k.SubscriptionID,
			MaxConcurrency: k.MaxConcurrency, Rpm: k.Rpm, Enabled: req.Body.Enabled, Note: k.Note})
		if err != nil {
			return err
		}
		return tx.InvalidateHashes(ctx, [][]byte{k.KeyHash})
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	s.audit(ctx, "key.set_enabled", "api_key", k.ID.String(), map[string]any{"enabled": k.Enabled, "user_id": k.UserID},
		map[string]any{"enabled": row.Enabled, "user_id": k.UserID})
	return AdminSetKeyEnabled200JSONResponse(keyOf(row)), nil
}

// ---- overview -------------------------------------------------------------------------------

func (s *Server) location() *time.Location {
	if s.Location != nil {
		return s.Location
	}
	return time.UTC
}

// DayBounds returns the start and end (exclusive) of the day containing now
// in loc, as UTC instants. Days are cut at local midnight, so DST days are
// 23 or 25 hours long.
func DayBounds(now time.Time, loc *time.Location) (time.Time, time.Time) {
	l := now.In(loc)
	start := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	end := time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc)
	return start.UTC(), end.UTC()
}

var tierOrder = map[string]int{router.TierPlan: 0, router.TierStandard: 1, router.TierForce: 2}

// OverviewTiers orders per-tier totals plan, standard, force, then the rest
// (by name) and sums them.
func OverviewTiers(rows []sqlc.RequestTotalsByTierRow) ([]OverviewTierTotals, int64, int64, int64) {
	out := make([]OverviewTierTotals, len(rows))
	var req, billed, upstream int64
	for i, r := range rows {
		out[i] = OverviewTierTotals{Tier: r.Tier, Requests: r.Requests, BilledMicro: r.BilledMicro, UpstreamCostMicro: r.UpstreamCostMicro}
		req += r.Requests
		billed += r.BilledMicro
		upstream += r.UpstreamCostMicro
	}
	rank := func(t string) int {
		if v, ok := tierOrder[t]; ok {
			return v
		}
		return 3
	}
	slices.SortStableFunc(out, func(a, b OverviewTierTotals) int {
		if d := rank(a.Tier) - rank(b.Tier); d != 0 {
			return d
		}
		switch {
		case a.Tier < b.Tier:
			return -1
		case a.Tier > b.Tier:
			return 1
		}
		return 0
	})
	return out, req, billed, upstream
}

// CountAccounts tallies accounts by their (disjoint) overview status and sums
// headroom and reservations.
func CountAccounts(items []Account) OverviewAccounts {
	var out OverviewAccounts
	for _, a := range items {
		out.Total++
		rt := a.Runtime
		out.ReservedMicro += rt.ReservedMicro
		switch rt.Status {
		case AccountRuntimeStatusDisabled:
			out.Disabled++
			continue
		case AccountRuntimeStatusError:
			out.Error++
			continue
		case AccountRuntimeStatusCooldown:
			out.Cooldown++
		case AccountRuntimeStatusStale:
			out.Stale++
		default:
			if rt.Penalized {
				out.Penalized++
			} else {
				out.Normal++
			}
		}
		out.HeadroomMicro += max(0, rt.HeadroomMicro)
	}
	return out
}

// streamBacklog is lag + pending of the settlement consumer group, or the
// stream length while the group does not exist.
func (s *Server) streamBacklog(ctx context.Context) (int64, error) {
	groups, err := s.St.R.XInfoGroups(ctx, s.St.StreamKey()).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) || err.Error() == "ERR no such key" {
			return 0, nil
		}
		return 0, err
	}
	for _, g := range groups {
		if g.Name == settle.Group {
			return max(0, g.Lag) + g.Pending, nil
		}
	}
	return s.St.R.XLen(ctx, s.St.StreamKey()).Result()
}

// AdminGetOverview implements GET /admin/overview.
func (s *Server) AdminGetOverview(ctx context.Context, _ AdminGetOverviewRequestObject) (AdminGetOverviewResponseObject, error) {
	now := time.Now().UTC()
	loc := s.location()
	from, to := DayBounds(now, loc)
	totals, err := s.Store.Q.RequestTotalsByTier(ctx, sqlc.RequestTotalsByTierParams{FromAt: from, ToAt: to})
	if err != nil {
		return nil, err
	}
	byTier, req, billed, upstream := OverviewTiers(totals)
	local := now.In(loc)
	out := AdminOverview{GeneratedAt: now, Timezone: loc.String(),
		Today:         OverviewToday{From: from, To: to, Requests: req, BilledMicro: billed, UpstreamCostMicro: upstream, ByTier: byTier},
		LastReconcile: nullable.NewNullNullable[ReconcileRun]()}
	out.Today.Day.Time = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)

	rows, err := s.Store.Q.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	states := s.accountStates(ctx)
	accounts := make([]Account, len(rows))
	for i, row := range rows {
		accounts[i] = Account{Id: row.ID, Runtime: s.runtimeOf(ctx, row, states[row.ID], now)}
	}
	out.Accounts = CountAccounts(accounts)

	if out.PendingAssignment, err = s.Store.Q.CountSubscriptionsByStatus(ctx, "pending_assignment"); err != nil {
		return nil, err
	}
	if out.StreamBacklog, err = s.streamBacklog(ctx); err != nil {
		s.Log.Warn("stream backlog", "err", err)
	}
	if run, err := s.Store.Q.LastReconcileRun(ctx); err == nil {
		out.LastReconcile = nullable.NewNullableWithValue(ReconcileRun{Id: run.ID, StartedAt: run.StartedAt.UTC(),
			FinishedAt: run.FinishedAt.UTC(), Ok: run.Ok, BalanceMismatches: int(run.BalanceMismatches), ChargeDiffMicro: run.ChargeDiffMicro})
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return AdminGetOverview200JSONResponse(out), nil
}
