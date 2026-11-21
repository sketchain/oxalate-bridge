// Package router decides which tier serves a request and which upstream
// account it goes to.
package router

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/secret"
)

// Account is the routing view of an upstream account.
type Account struct {
	ID                 uuid.UUID
	Name               string
	BaseURL            string
	CPAKey             string
	Enabled            bool
	Error              bool
	ErrorReason        string
	Watermark5hPercent *float64
	MaxConcurrency     *int32
	Subscription       string
	Instance           string
	AuthIndex          string
	Provider           string
	Tier               string
}

// Subscription is the routing view of a live plan subscription.
type Subscription struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	Status        string
	AccountID     *uuid.UUID
	Subscription  string
	WeeklyQuota   int64
	AllowedModels map[string]bool
	ActivatedAt   *time.Time
	ExpiresAt     *time.Time
}

// Catalog caches accounts, live subscriptions, quota snapshots and model
// lists in memory. Accounts and subscriptions reload from PostgreSQL on a
// routing change notice and periodically; snapshots and models reload from
// Redis every few seconds.
type Catalog struct {
	q   *sqlc.Queries
	st  *rstate.State
	box *secret.Box
	log *slog.Logger

	mu        sync.RWMutex
	accounts  map[uuid.UUID]*Account
	order     []uuid.UUID
	subs      map[uuid.UUID]*Subscription
	byAccount map[uuid.UUID][]uuid.UUID
	snapshots map[uuid.UUID]*cpa.Snapshot
	models    map[uuid.UUID]map[string]bool
	modelIDs  map[uuid.UUID][]string
	periods   map[uuid.UUID]Period
}

// NewCatalog builds an empty catalog.
func NewCatalog(q *sqlc.Queries, st *rstate.State, box *secret.Box, log *slog.Logger) *Catalog {
	return &Catalog{q: q, st: st, box: box, log: log,
		accounts: map[uuid.UUID]*Account{}, subs: map[uuid.UUID]*Subscription{}, byAccount: map[uuid.UUID][]uuid.UUID{},
		snapshots: map[uuid.UUID]*cpa.Snapshot{}, models: map[uuid.UUID]map[string]bool{}, modelIDs: map[uuid.UUID][]string{},
		periods: map[uuid.UUID]Period{}}
}

// ReloadDB reloads accounts and live subscriptions.
func (c *Catalog) ReloadDB(ctx context.Context) error {
	rows, err := c.q.ListAccounts(ctx)
	if err != nil {
		return err
	}
	accounts := make(map[uuid.UUID]*Account, len(rows))
	order := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		key, err := c.box.Open(r.CpaKeyEnc)
		if err != nil {
			c.log.Error("decrypt account key", "account", r.ID, "err", err)
			continue
		}
		accounts[r.ID] = &Account{ID: r.ID, Name: r.Name, BaseURL: r.BaseUrl, CPAKey: key, Enabled: r.Enabled,
			Error: r.ErrorStatus, ErrorReason: r.ErrorReason, Watermark5hPercent: r.Watermark5hPercent,
			MaxConcurrency: r.MaxConcurrency, Subscription: r.Subscription, Instance: r.Instance, AuthIndex: r.AuthIndex,
			Provider: r.Provider, Tier: r.Tier}
		order = append(order, r.ID)
	}
	subRows, err := c.q.ListLiveSubscriptions(ctx)
	if err != nil {
		return err
	}
	subs := make(map[uuid.UUID]*Subscription, len(subRows))
	byAccount := map[uuid.UUID][]uuid.UUID{}
	for _, s := range subRows {
		allowed := map[string]bool{}
		for _, m := range s.AllowedModels {
			allowed[billing.NormalizeModel(m)] = true
		}
		subs[s.ID] = &Subscription{ID: s.ID, UserID: s.UserID, Status: s.Status, AccountID: s.AccountID,
			Subscription: s.Subscription, WeeklyQuota: s.WeeklyQuotaMicro, AllowedModels: allowed,
			ActivatedAt: s.ActivatedAt, ExpiresAt: s.ExpiresAt}
		if s.AccountID != nil && s.Status == "active" {
			byAccount[*s.AccountID] = append(byAccount[*s.AccountID], s.ID)
		}
	}
	c.mu.Lock()
	c.accounts, c.order, c.subs, c.byAccount = accounts, order, subs, byAccount
	c.mu.Unlock()
	return nil
}

// ReloadRedis reloads snapshots and model lists of all known accounts.
func (c *Catalog) ReloadRedis(ctx context.Context) error {
	c.mu.RLock()
	ids := append([]uuid.UUID(nil), c.order...)
	subIDs := make([]uuid.UUID, 0, len(c.subs))
	for id := range c.subs {
		subIDs = append(subIDs, id)
	}
	c.mu.RUnlock()
	if err := c.reloadPeriods(ctx, subIDs); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	keys := make([]string, 0, 2*len(ids))
	for _, id := range ids {
		keys = append(keys, c.st.QuotaKey(id), c.st.ModelsKey(id))
	}
	vals, err := c.st.R.MGet(ctx, keys...).Result()
	if err != nil && err != redis.Nil {
		return err
	}
	snaps := make(map[uuid.UUID]*cpa.Snapshot, len(ids))
	models := make(map[uuid.UUID]map[string]bool, len(ids))
	modelIDs := make(map[uuid.UUID][]string, len(ids))
	for i, id := range ids {
		if s, ok := vals[2*i].(string); ok {
			var snap cpa.Snapshot
			if json.Unmarshal([]byte(s), &snap) == nil {
				snaps[id] = &snap
			}
		}
		if s, ok := vals[2*i+1].(string); ok {
			var list []string
			if json.Unmarshal([]byte(s), &list) == nil {
				set := make(map[string]bool, len(list))
				for _, m := range list {
					set[billing.NormalizeModel(m)] = true
				}
				models[id] = set
				modelIDs[id] = list
			}
		}
	}
	c.mu.Lock()
	c.snapshots, c.models, c.modelIDs = snaps, models, modelIDs
	c.mu.Unlock()
	return nil
}

// Run keeps the catalog fresh until ctx ends. reloadDB receives routing
// change notices.
func (c *Catalog) Run(ctx context.Context, reloadDB <-chan struct{}) {
	dbTick := time.NewTicker(30 * time.Second)
	redisTick := time.NewTicker(2 * time.Second)
	defer dbTick.Stop()
	defer redisTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-reloadDB:
			if err := c.ReloadDB(ctx); err != nil {
				c.log.Warn("reload catalog", "err", err)
			}
			_ = c.ReloadRedis(ctx)
		case <-dbTick.C:
			if err := c.ReloadDB(ctx); err != nil {
				c.log.Warn("reload catalog", "err", err)
			}
		case <-redisTick.C:
			if err := c.ReloadRedis(ctx); err != nil {
				c.log.Warn("reload snapshots", "err", err)
			}
		}
	}
}

// Account returns an account by id.
func (c *Catalog) Account(id uuid.UUID) (*Account, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	a, ok := c.accounts[id]
	return a, ok
}

// Accounts returns all accounts in creation order.
func (c *Catalog) Accounts() []*Account {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Account, 0, len(c.order))
	for _, id := range c.order {
		if a, ok := c.accounts[id]; ok {
			out = append(out, a)
		}
	}
	return out
}

// Subscription returns a live subscription by id.
func (c *Catalog) Subscription(id uuid.UUID) (*Subscription, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.subs[id]
	return s, ok
}

// SubscriptionsOn returns the active subscriptions bound to an account.
func (c *Catalog) SubscriptionsOn(account uuid.UUID) []*Subscription {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := c.byAccount[account]
	out := make([]*Subscription, 0, len(ids))
	for _, id := range ids {
		if s, ok := c.subs[id]; ok {
			out = append(out, s)
		}
	}
	return out
}

// Snapshot returns an account's latest quota snapshot.
func (c *Catalog) Snapshot(id uuid.UUID) *cpa.Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshots[id]
}

// SetSnapshot updates one snapshot in memory (poller on this replica).
func (c *Catalog) SetSnapshot(id uuid.UUID, s *cpa.Snapshot) {
	c.mu.Lock()
	c.snapshots[id] = s
	c.mu.Unlock()
}

// Serves reports whether an account's cached model list has the model.
func (c *Catalog) Serves(id uuid.UUID, normModel string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.models[id][normModel]
}

// ModelIDs returns an account's cached model ids.
func (c *Catalog) ModelIDs(id uuid.UUID) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.modelIDs[id]
}

// SetAccountsForTest installs accounts and subscriptions directly.
func (c *Catalog) SetForTest(accounts []*Account, subs []*Subscription, snaps map[uuid.UUID]*cpa.Snapshot, models map[uuid.UUID][]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accounts, c.order = map[uuid.UUID]*Account{}, nil
	for _, a := range accounts {
		c.accounts[a.ID] = a
		c.order = append(c.order, a.ID)
	}
	c.subs, c.byAccount = map[uuid.UUID]*Subscription{}, map[uuid.UUID][]uuid.UUID{}
	for _, s := range subs {
		c.subs[s.ID] = s
		if s.AccountID != nil && s.Status == "active" {
			c.byAccount[*s.AccountID] = append(c.byAccount[*s.AccountID], s.ID)
		}
	}
	c.snapshots = snaps
	c.models, c.modelIDs = map[uuid.UUID]map[string]bool{}, models
	for id, list := range models {
		set := map[string]bool{}
		for _, m := range list {
			set[billing.NormalizeModel(m)] = true
		}
		c.models[id] = set
	}
}

func (c *Catalog) reloadPeriods(ctx context.Context, subIDs []uuid.UUID) error {
	periods := make(map[uuid.UUID]Period, len(subIDs))
	if len(subIDs) > 0 {
		keys := make([]string, len(subIDs))
		for i, id := range subIDs {
			keys[i] = c.st.PeriodKey(id)
		}
		vals, err := c.st.R.MGet(ctx, keys...).Result()
		if err != nil && err != redis.Nil {
			return err
		}
		for i, v := range vals {
			if s, ok := v.(string); ok {
				var p Period
				if json.Unmarshal([]byte(s), &p) == nil {
					periods[subIDs[i]] = p
				}
			}
		}
	}
	c.mu.Lock()
	c.periods = periods
	c.mu.Unlock()
	return nil
}

// PeriodFor returns a subscription's current plan period, deriving it from
// the bound account's week window and persisting it when it changes.
func (c *Catalog) PeriodFor(ctx context.Context, sub *Subscription, now time.Time) Period {
	var weekReset *time.Time
	if sub.AccountID != nil {
		if snap := c.Snapshot(*sub.AccountID); snap != nil && snap.HasData && snap.Account.Week != nil {
			weekReset = snap.Account.Week.ResetAt
		}
	}
	anchor := now
	if sub.ActivatedAt != nil {
		anchor = *sub.ActivatedAt
	}
	c.mu.RLock()
	prev, hasPrev := c.periods[sub.ID]
	c.mu.RUnlock()
	var prevPtr *Period
	if hasPrev {
		prevPtr = &prev
	}
	p := CurrentPeriod(weekReset, anchor, now, prevPtr)
	if !hasPrev || p != prev {
		c.mu.Lock()
		c.periods[sub.ID] = p
		c.mu.Unlock()
		if raw, err := json.Marshal(p); err == nil {
			_ = c.st.R.Set(ctx, c.st.PeriodKey(sub.ID), raw, 30*24*time.Hour).Err()
		}
	}
	return p
}
