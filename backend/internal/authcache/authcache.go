// Package authcache resolves API keys with a two-level cache: an in-process
// L1 with a short TTL and a Redis L2. PostgreSQL is only read on an L2 miss.
// Invalidations are written to the outbox in the same transaction as the key
// or user change; the outbox dispatcher deletes the L2 entries and publishes
// the key hashes so every replica drops its L1 entries.
package authcache

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/secret"
)

// Info is everything the request path needs about a key and its user.
type Info struct {
	Found          bool       `json:"found"`
	KeyID          uuid.UUID  `json:"key_id"`
	UserID         uuid.UUID  `json:"user_id"`
	Tiers          []string   `json:"tiers"`
	SubscriptionID *uuid.UUID `json:"subscription_id,omitempty"`
	KeyMaxConc     *int32     `json:"key_max_concurrency,omitempty"`
	KeyRPM         *int32     `json:"key_rpm,omitempty"`
	UserMaxConc    int32      `json:"user_max_concurrency"`
	UserRPM        *int32     `json:"user_rpm,omitempty"`
	Role           string     `json:"role"`
	KeyEnabled     bool       `json:"key_enabled"`
	UserActive     bool       `json:"user_active"`

	// Key limits (nil / empty = unlimited).
	QuotaLimit      *int64     `json:"quota_limit,omitempty"`
	QuotaScope      string     `json:"quota_scope,omitempty"`
	Limit5h         *int64     `json:"limit_5h,omitempty"`
	Limit1d         *int64     `json:"limit_1d,omitempty"`
	Limit7d         *int64     `json:"limit_7d,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	ModelAllowlist  []string   `json:"model_allowlist,omitempty"`
	IPAllowlist     []string   `json:"ip_allowlist,omitempty"`
	AllowOwnerQuery bool       `json:"allow_owner_query,omitempty"`
}

// Expired reports whether the key has expired at now.
func (i Info) Expired(now time.Time) bool { return i.ExpiresAt != nil && !now.Before(*i.ExpiresAt) }

// Scope is the meter scope of the key's limits: "a" (all) or "b" (balance).
func (i Info) Scope() string {
	if i.QuotaScope == "balance" {
		return "b"
	}
	return "a"
}

// Counts reports whether a charge of tier counts toward the key's limits.
func (i Info) Counts(tier string) bool {
	return i.Scope() == "a" || tier == "standard" || tier == "force"
}

// Limits returns the key's limits in meter form (negative = unlimited).
func (i Info) Limits() rstate.KeyLimits {
	v := func(p *int64) int64 {
		if p == nil {
			return -1
		}
		return *p
	}
	return rstate.KeyLimits{Scope: i.Scope(), Quota: v(i.QuotaLimit), Windows: [3]int64{v(i.Limit5h), v(i.Limit1d), v(i.Limit7d)}}
}

// Usable reports whether the key may make requests.
func (i Info) Usable() bool { return i.Found && i.KeyEnabled && i.UserActive }

type l1Entry struct {
	info    Info
	expires time.Time
}

// Cache is the two-level key cache.
type Cache struct {
	q     *sqlc.Queries
	st    *rstate.State
	log   *slog.Logger
	L1TTL time.Duration
	L2TTL time.Duration
	// NegTTL bounds how long an unknown key is remembered.
	NegTTL time.Duration

	mu sync.Mutex
	l1 map[string]l1Entry
	// gen is bumped by every invalidation so a lookup racing with one does
	// not store a stale value in L1.
	gen uint64
}

// New returns a cache.
func New(q *sqlc.Queries, st *rstate.State, log *slog.Logger) *Cache {
	return &Cache{q: q, st: st, log: log, L1TTL: 10 * time.Second, L2TTL: 10 * time.Minute, NegTTL: 30 * time.Second,
		l1: map[string]l1Entry{}}
}

// HashHex is the cache key of a raw API key.
func HashHex(key string) string { return hex.EncodeToString(secret.KeyHash(key)) }

// Lookup resolves a raw API key.
func (c *Cache) Lookup(ctx context.Context, key string) (Info, error) {
	h := HashHex(key)
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.l1[h]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.info, nil
	}
	gen := c.gen
	c.mu.Unlock()

	info, err := c.lookupL2(ctx, h)
	if err != nil {
		return Info{}, err
	}
	c.mu.Lock()
	if c.gen == gen {
		ttl := c.L1TTL
		if !info.Found {
			ttl = min(ttl, c.NegTTL)
		}
		c.l1[h] = l1Entry{info: info, expires: now.Add(ttl)}
		if len(c.l1) > 100_000 {
			c.sweepLocked(now)
		}
	}
	c.mu.Unlock()
	return info, nil
}

func (c *Cache) sweepLocked(now time.Time) {
	for k, e := range c.l1 {
		if now.After(e.expires) {
			delete(c.l1, k)
		}
	}
}

func (c *Cache) lookupL2(ctx context.Context, h string) (Info, error) {
	raw, err := c.st.R.Get(ctx, c.st.AuthKey(h)).Bytes()
	if err == nil {
		var info Info
		if json.Unmarshal(raw, &info) == nil {
			return info, nil
		}
	} else if !errors.Is(err, redis.Nil) {
		return Info{}, err
	}
	info, err := c.loadPG(ctx, h)
	if err != nil {
		return Info{}, err
	}
	ttl := c.L2TTL
	if !info.Found {
		ttl = c.NegTTL
	}
	if raw, err := json.Marshal(info); err == nil {
		// SET NX: an invalidation that deleted the key after our PG read
		// wins only if it lands after this write; the outbox dispatcher
		// deletes again after publishing to close that window.
		_ = c.st.R.SetNX(ctx, c.st.AuthKey(h), raw, ttl).Err()
	}
	return info, nil
}

func (c *Cache) loadPG(ctx context.Context, h string) (Info, error) {
	hash, err := hex.DecodeString(h)
	if err != nil {
		return Info{}, err
	}
	row, err := c.q.GetAuthByKeyHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Info{Found: false}, nil
	}
	if err != nil {
		return Info{}, err
	}
	return Info{Found: !row.KeyDeleted, KeyID: row.KeyID, UserID: row.UserID, Tiers: row.Tiers,
		SubscriptionID: row.SubscriptionID, KeyMaxConc: row.KeyMaxConcurrency, KeyRPM: row.KeyRpm,
		UserMaxConc: row.UserMaxConcurrency, UserRPM: row.UserRpm, Role: row.Role,
		KeyEnabled: row.KeyEnabled, UserActive: row.UserStatus == "active",
		QuotaLimit: row.QuotaLimitMicro, QuotaScope: row.QuotaScope, Limit5h: row.Limit5hMicro, Limit1d: row.Limit1dMicro,
		Limit7d: row.Limit7dMicro, ExpiresAt: row.ExpiresAt, ModelAllowlist: row.ModelAllowlist, IPAllowlist: row.IpAllowlist,
		AllowOwnerQuery: row.AllowOwnerQuery}, nil
}

// DropL1 removes key hashes from this replica's L1 (pub/sub handler).
func (c *Cache) DropL1(hashes []string) {
	c.mu.Lock()
	c.gen++
	for _, h := range hashes {
		delete(c.l1, h)
	}
	c.mu.Unlock()
}

// DropAllL1 clears this replica's L1.
func (c *Cache) DropAllL1() {
	c.mu.Lock()
	c.gen++
	c.l1 = map[string]l1Entry{}
	c.mu.Unlock()
}

// Invalidation is the outbox / pub/sub payload.
type Invalidation struct {
	Hashes []string `json:"hashes"`
}

// Deliver deletes L2 entries and broadcasts the L1 drop. It is called by the
// outbox dispatcher.
func (c *Cache) Deliver(ctx context.Context, inv Invalidation) error {
	if len(inv.Hashes) == 0 {
		return nil
	}
	keys := make([]string, len(inv.Hashes))
	for i, h := range inv.Hashes {
		keys[i] = c.st.AuthKey(h)
	}
	if err := c.st.R.Del(ctx, keys...).Err(); err != nil {
		return err
	}
	raw, _ := json.Marshal(inv)
	if err := c.st.R.Publish(ctx, c.st.ChanInvalidate(), raw).Err(); err != nil {
		return err
	}
	// Second delete closes the race with a lookup that read PG before the
	// change committed and wrote L2 after the first delete.
	time.AfterFunc(2*time.Second, func() { c.st.R.Del(context.Background(), keys...) })
	return nil
}
