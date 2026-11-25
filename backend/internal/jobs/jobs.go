// Package jobs runs the background tasks. A Redis lock with renewal elects
// one leader among replicas; only the leader runs the scheduled tasks.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/sketchain/oxalate-bridge/backend/internal/avail"
	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/db"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/events"
	"github.com/sketchain/oxalate-bridge/backend/internal/metrics"
	"github.com/sketchain/oxalate-bridge/backend/internal/notify"
	"github.com/sketchain/oxalate-bridge/backend/internal/router"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/safeguard"
	"github.com/sketchain/oxalate-bridge/backend/internal/secret"
	"github.com/sketchain/oxalate-bridge/backend/internal/settings"
	"github.com/sketchain/oxalate-bridge/backend/internal/settle"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
	"github.com/sketchain/oxalate-bridge/backend/internal/subs"
)

// Runner owns the leader lock and the scheduled tasks.
type Runner struct {
	St       *rstate.State
	Store    *store.Store
	Box      *secret.Box
	CPA      *cpa.Client
	Cat      *router.Catalog
	Router   *router.Router
	Settings *settings.Store
	Prices   *billing.Prices
	Subs     *subs.Service
	Hub      *events.Hub
	Log      *slog.Logger
	ID       string
	// RefreshPricesFn fetches the ratio table and stores it in PG and Redis.
	RefreshPricesFn func(context.Context) error
	// Avail is recomputed after every quota poll.
	Avail *avail.Service
	// Notify detects state changes and sends push notifications (nil = off).
	Notify *notify.Service
	// Safeguard keeps the Redis prompt blocklist in step with PG (nil = off).
	Safeguard *safeguard.Service

	leader atomic.Bool
	mu     sync.Mutex
	last   map[string]time.Time
}

// IsLeader reports whether this replica holds the lock.
func (r *Runner) IsLeader() bool { return r.leader.Load() }

const leaderTTL = 15 * time.Second

var renewScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('PEXPIRE', KEYS[1], ARGV[2]) end
return 0
`)

var releaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0
`)

func (r *Runner) campaign(ctx context.Context) {
	key := r.St.LeaderKey()
	if r.leader.Load() {
		n, err := renewScript.Run(ctx, r.St.R, []string{key}, r.ID, leaderTTL.Milliseconds()).Int()
		if err != nil || n == 0 {
			r.leader.Store(false)
			r.Log.Warn("lost leadership", "err", err)
		}
		return
	}
	ok, err := r.St.R.SetNX(ctx, key, r.ID, leaderTTL).Result()
	if err == nil && ok {
		r.leader.Store(true)
		r.Log.Info("acquired leadership", "id", r.ID)
	}
}

// Run campaigns and runs due tasks until ctx ends.
func (r *Runner) Run(ctx context.Context) {
	r.last = map[string]time.Time{}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastCampaign := time.Time{}
	var wg sync.WaitGroup
	running := map[string]*atomic.Bool{}
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			if r.leader.Load() {
				_ = releaseScript.Run(context.Background(), r.St.R, []string{r.St.LeaderKey()}, r.ID).Err()
			}
			return
		case <-tick.C:
		}
		if time.Since(lastCampaign) >= 5*time.Second {
			lastCampaign = time.Now()
			r.campaign(ctx)
		}
		if !r.leader.Load() {
			continue
		}
		set := r.Settings.Get()
		for _, t := range r.tasks(set) {
			if !r.due(t.name, t.every) {
				continue
			}
			flag := running[t.name]
			if flag == nil {
				flag = &atomic.Bool{}
				running[t.name] = flag
			}
			if !flag.CompareAndSwap(false, true) {
				continue
			}
			wg.Add(1)
			go func(t task) {
				defer wg.Done()
				defer flag.Store(false)
				tctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
				defer cancel()
				if err := t.fn(tctx); err != nil && ctx.Err() == nil {
					r.Log.Warn("job failed", "job", t.name, "err", err)
				}
			}(t)
		}
	}
}

type task struct {
	name  string
	every time.Duration
	fn    func(context.Context) error
}

func (r *Runner) tasks(set settings.Values) []task {
	return []task{
		{"quota", time.Duration(set.QuotaPollSeconds) * time.Second, r.PollQuotas},
		{"models", time.Duration(set.ModelsPollMinutes) * time.Minute, r.PollModels},
		{"prices", time.Hour, r.maybeRefreshPrices},
		{"subscriptions", time.Minute, r.subscriptions},
		{"partitions", 6 * time.Hour, r.partitions},
		{"reconcile", time.Hour, r.maybeReconcile},
		{"stream", 30 * time.Second, r.StreamMaintenance},
		{"headroom", 30 * time.Second, r.headroomMetrics},
		{"notify", 30 * time.Second, r.notifyScan},
		{"safeguard", 5 * time.Minute, r.syncBlocklist},
	}
}

func (r *Runner) due(name string, every time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if last, ok := r.last[name]; ok && time.Since(last) < every {
		return false
	}
	r.last[name] = time.Now()
	return true
}

func (r *Runner) syncBlocklist(ctx context.Context) error {
	if r.Safeguard == nil {
		return nil
	}
	return r.Safeguard.SyncBlocklist(ctx)
}

// ---- quota and models ------------------------------------------------------

// PollQuotas fetches /quota for every enabled account.
func (r *Runner) PollQuotas(ctx context.Context) error {
	accounts, err := r.Store.Q.ListAccounts(ctx)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	changed := atomic.Bool{}
	for _, a := range accounts {
		if !a.Enabled {
			continue
		}
		wg.Add(1)
		go func(a sqlc.Account) {
			defer wg.Done()
			if r.pollQuota(ctx, a) {
				changed.Store(true)
			}
		}(a)
	}
	wg.Wait()
	if changed.Load() {
		_ = r.Store.InTx(ctx, func(tx *store.Tx) error { return tx.RoutingChanged(ctx) })
	}
	if r.Avail != nil {
		if _, err := r.Avail.Refresh(ctx); err != nil {
			r.Log.Warn("refresh availability", "err", err)
		}
	}
	return nil
}

func (r *Runner) loadSnapshot(ctx context.Context, id uuid.UUID) *cpa.Snapshot {
	raw, err := r.St.R.Get(ctx, r.St.QuotaKey(id)).Bytes()
	if err != nil {
		return &cpa.Snapshot{}
	}
	var s cpa.Snapshot
	if json.Unmarshal(raw, &s) != nil {
		return &cpa.Snapshot{}
	}
	return &s
}

// pollQuota refreshes one account and reports whether its quota identity
// fields changed in PostgreSQL.
func (r *Runner) pollQuota(ctx context.Context, a sqlc.Account) bool {
	now := time.Now().UTC()
	snap := r.loadSnapshot(ctx, a.ID)
	token, err := r.Box.Open(a.QuotaTokenEnc)
	var resp *cpa.QuotaResponse
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		resp, err = r.CPA.Quota(cctx, a.BaseUrl, token)
		cancel()
	}
	changed := false
	snap.LastAttemptAt = now
	if err != nil {
		snap.LastError = err.Error()
		if snap.FailingSince.IsZero() {
			snap.FailingSince = now
		}
	} else {
		acc := resp.Accounts[0]
		*snap = cpa.Snapshot{FetchedAt: now, LastAttemptAt: now, DatabaseStatus: resp.Database.Status,
			EstimatesReady: resp.Estimates.Loaded, Instance: resp.Instance, Tier: resp.Tier, Account: acc, HasData: true}
		if a.Instance != resp.Instance || a.Tier != resp.Tier || a.AuthIndex != acc.AuthIndex {
			if err := r.Store.Q.UpdateAccountQuotaInfo(ctx, sqlc.UpdateAccountQuotaInfoParams{ID: a.ID, Instance: resp.Instance,
				Tier: resp.Tier, AuthIndex: acc.AuthIndex}); err == nil {
				changed = true
			}
		}
	}
	raw, _ := json.Marshal(snap)
	if err := r.St.R.Set(ctx, r.St.QuotaKey(a.ID), raw, 0).Err(); err != nil {
		r.Log.Warn("store snapshot", "account", a.ID, "err", err)
	}
	r.Cat.SetSnapshot(a.ID, snap)
	staleAfter := time.Duration(r.Settings.Get().StaleAfterSeconds) * time.Second
	r.Hub.Publish(ctx, "account_status", map[string]any{
		"account_id": a.ID, "name": a.Name, "fetched_at": snap.FetchedAt, "last_error": snap.LastError,
		"stale": snap.Stale(now, staleAfter), "snapshot": snap,
	})
	return changed
}

// PollModels refreshes the model list of every enabled account.
func (r *Runner) PollModels(ctx context.Context) error {
	for _, a := range r.Cat.Accounts() {
		if !a.Enabled {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		ids, err := r.CPA.Models(cctx, a.BaseURL, a.CPAKey)
		cancel()
		if err != nil {
			r.Log.Warn("poll models", "account", a.ID, "err", err)
			continue
		}
		raw, _ := json.Marshal(ids)
		_ = r.St.R.Set(ctx, r.St.ModelsKey(a.ID), raw, 0).Err()
	}
	return r.Cat.ReloadRedis(ctx)
}

// ---- prices -----------------------------------------------------------------

func (r *Runner) maybeRefreshPrices(ctx context.Context) error {
	set := r.Settings.Get()
	st := r.Prices.Status()
	if r.Prices.Table() != nil && st.Origin != "none" && time.Since(st.FetchedAt) < time.Duration(set.PriceRefreshHours)*time.Hour {
		return nil
	}
	return r.RefreshPrices(ctx)
}

// RefreshPrices fetches the ratio table now and tells replicas to reload.
func (r *Runner) RefreshPrices(ctx context.Context) error {
	if r.RefreshPricesFn != nil {
		return r.RefreshPricesFn(ctx)
	}
	if err := r.Prices.Refresh(ctx, r.Store.Q, r.Settings.Get().PriceURL); err != nil {
		return err
	}
	return r.St.R.Publish(ctx, r.St.ChanSettings(), "prices").Err()
}

// ---- subscriptions, partitions, reconcile -----------------------------------

func (r *Runner) subscriptions(ctx context.Context) error {
	lead := time.Duration(r.Settings.Get().AutoRenewLeadHours) * time.Hour
	renewed, failed, err := r.Subs.AutoRenewDue(ctx, lead)
	if err != nil {
		return err
	}
	if renewed+failed > 0 {
		r.Log.Info("auto renew", "renewed", renewed, "failed", failed)
	}
	n, err := r.Subs.ExpireDue(ctx)
	if n > 0 {
		r.Log.Info("subscriptions expired", "n", n)
	}
	return err
}

func (r *Runner) partitions(ctx context.Context) error {
	return db.EnsurePartitions(ctx, r.Store.Pool, time.Now().UTC(), 2)
}

func (r *Runner) maybeReconcile(ctx context.Context) error {
	last, err := r.Store.Q.LastReconcileRun(ctx)
	if err == nil && time.Since(last.StartedAt) < 24*time.Hour {
		return nil
	}
	res, err := Reconcile(ctx, r.Store, r.Log)
	if err == nil && r.Notify != nil {
		if nerr := r.Notify.ObserveReconcile(ctx, res.OK, len(res.BalanceMismatches), res.BilledTotal-res.ChargeTotal); nerr != nil {
			r.Log.Warn("notify reconcile", "err", nerr)
		}
	}
	return err
}

// ReconcileResult is the outcome of a reconcile run.
type ReconcileResult struct {
	OK                bool                            `json:"ok"`
	BalanceMismatches []sqlc.ListUserBalanceChecksRow `json:"balance_mismatches"`
	BilledTotal       int64                           `json:"billed_total_micro"`
	ChargeTotal       int64                           `json:"api_charge_total_micro"`
}

// Reconcile checks ledger sums against balances and billed request totals
// against api_charge entries, records the run and updates the metrics.
func Reconcile(ctx context.Context, s *store.Store, log *slog.Logger) (ReconcileResult, error) {
	started := time.Now().UTC()
	var res ReconcileResult
	err := s.InTx(ctx, func(tx *store.Tx) error {
		// One snapshot for all three reads.
		if _, err := tx.Raw.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
			return err
		}
		var err error
		if res.BalanceMismatches, err = tx.Q.ListUserBalanceChecks(ctx); err != nil {
			return err
		}
		if res.BilledTotal, err = tx.Q.SumBilledBalanceTiers(ctx); err != nil {
			return err
		}
		res.ChargeTotal, err = tx.Q.SumAPICharges(ctx)
		return err
	})
	if err != nil {
		return res, err
	}
	diff := res.BilledTotal - res.ChargeTotal
	res.OK = len(res.BalanceMismatches) == 0 && diff == 0
	metrics.ReconcileBalanceMismatches.Set(float64(len(res.BalanceMismatches)))
	metrics.ReconcileChargeDiff.Set(float64(diff))
	detail, _ := json.Marshal(res)
	id, _ := uuid.NewV7()
	if err := s.Q.InsertReconcileRun(ctx, sqlc.InsertReconcileRunParams{ID: id, StartedAt: started, FinishedAt: time.Now().UTC(),
		Ok: res.OK, BalanceMismatches: int32(len(res.BalanceMismatches)), ChargeDiffMicro: diff, Detail: detail}); err != nil {
		return res, err
	}
	if !res.OK {
		log.Error("reconcile mismatch", "balance_mismatches", len(res.BalanceMismatches), "charge_diff_micro", diff)
	} else {
		log.Info("reconcile ok", "billed_total_micro", res.BilledTotal)
	}
	return res, nil
}

// ---- stream and metrics -----------------------------------------------------

// StreamMaintenance updates the backlog metric and trims entries every
// consumer has acknowledged.
func (r *Runner) StreamMaintenance(ctx context.Context) error {
	groups, err := r.St.R.XInfoGroups(ctx, r.St.StreamKey()).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) || isNoKey(err) {
			return nil
		}
		return err
	}
	for _, g := range groups {
		if g.Name != settle.Group {
			continue
		}
		metrics.StreamLag.Set(float64(g.Lag + g.Pending))
		if r.Notify != nil {
			if err := r.Notify.ObserveBacklog(ctx, g.Lag+g.Pending, r.Settings.Get().StreamBacklogThreshold); err != nil {
				r.Log.Warn("notify backlog", "err", err)
			}
		}
		minID := g.LastDeliveredID
		if g.Pending > 0 {
			p, err := r.St.R.XPending(ctx, r.St.StreamKey(), settle.Group).Result()
			if err == nil && p.Lower != "" {
				minID = p.Lower
			}
		}
		if minID != "" && minID != "0-0" {
			_ = r.St.R.XTrimMinIDApprox(ctx, r.St.StreamKey(), minID, 0).Err()
		}
	}
	return nil
}

func isNoKey(err error) bool {
	return err != nil && (err.Error() == "ERR no such key" || errors.Is(err, redis.Nil))
}

func (r *Runner) headroomMetrics(ctx context.Context) error {
	states, err := r.Router.States(ctx, nil, time.Now().UTC())
	if err != nil {
		return err
	}
	metrics.Headroom.Reset()
	for _, st := range states {
		if st.HasWeek {
			metrics.Headroom.WithLabelValues(st.Account.Name).Set(billing.MicroToUSD(st.Headroom))
		}
	}
	return nil
}

// ---- notifications ------------------------------------------------------------

// notifyScan feeds the state-change detectors: accounts, balances, expiring
// plans and safeguard events.
func (r *Runner) notifyScan(ctx context.Context) error {
	if r.Notify == nil {
		return nil
	}
	obs, err := r.AccountObservations(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	var errs []error
	errs = append(errs, r.Notify.ObserveAccounts(ctx, obs))
	errs = append(errs, r.Notify.ObserveBalances(ctx))
	errs = append(errs, r.Notify.ObservePlanExpiring(ctx))
	errs = append(errs, r.Notify.ObserveSafeguard(ctx))
	return errors.Join(errs...)
}

// AccountObservations is the notification view of every account. Disabled
// accounts report no problem, so they re-arm while disabled.
func (r *Runner) AccountObservations(ctx context.Context, now time.Time) ([]notify.AccountObs, error) {
	rows, err := r.Store.Q.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	staleAfter := time.Duration(r.Settings.Get().StaleAfterSeconds) * time.Second
	out := make([]notify.AccountObs, 0, len(rows))
	for _, a := range rows {
		o := notify.AccountObs{ID: a.ID, Name: a.Name}
		if a.Enabled {
			o.AuthError, o.Reason = a.ErrorStatus, a.ErrorReason
			if snap := r.Cat.Snapshot(a.ID); snap != nil && snap.HasData {
				if snap.Stale(now, staleAfter) {
					o.Stale, o.Age = true, now.Sub(snap.FetchedAt)
				}
				switch _, reason := cpa.HardExhausted(snap.Account, now); reason {
				case "5h_exhausted":
					o.Full = "5h"
				case "week_exhausted":
					o.Full = "week"
				}
			}
		}
		out = append(out, o)
	}
	return out, nil
}
