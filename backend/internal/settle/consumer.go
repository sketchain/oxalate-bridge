package settle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/metrics"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
)

// Group is the consumer group name.
const Group = "bridge"

// Consumer persists settlement records. Several replicas may run consumers
// in the same group; each batch is one PostgreSQL transaction and entries
// are acknowledged only after it commits.
type Consumer struct {
	St        *rstate.State
	Store     *store.Store
	Log       *slog.Logger
	Name      string
	BatchSize int64
	Block     time.Duration
	MinIdle   time.Duration
	// OnSettled receives the summaries of newly persisted records.
	OnSettled func([]Summary)
}

// EnsureGroup creates the stream and group if needed.
func EnsureGroup(ctx context.Context, st *rstate.State) error {
	err := st.R.XGroupCreateMkStream(ctx, st.StreamKey(), Group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

type entry struct {
	id  string
	rec Record
	bad bool
}

// Run consumes until ctx ends.
func (c *Consumer) Run(ctx context.Context) {
	if c.BatchSize == 0 {
		c.BatchSize = 500
	}
	if c.Block == 0 {
		c.Block = 2 * time.Second
	}
	if c.MinIdle == 0 {
		c.MinIdle = time.Minute
	}
	if err := EnsureGroup(ctx, c.St); err != nil {
		c.Log.Error("create consumer group", "err", err)
	}
	// Our own pending entries first (left over from a previous run).
	c.drainPending(ctx)
	lastClaim := time.Now()
	for ctx.Err() == nil {
		streams, err := c.St.R.XReadGroup(ctx, &redis.XReadGroupArgs{Group: Group, Consumer: c.Name,
			Streams: []string{c.St.StreamKey(), ">"}, Count: c.BatchSize, Block: c.Block}).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			if ctx.Err() != nil {
				return
			}
			if strings.Contains(err.Error(), "NOGROUP") {
				_ = EnsureGroup(ctx, c.St)
			}
			c.Log.Warn("read settlement stream", "err", err)
			sleep(ctx, time.Second)
			continue
		}
		for _, s := range streams {
			if len(s.Messages) > 0 {
				_ = c.process(ctx, s.Messages) // failures stay pending and are retried
			}
		}
		if time.Since(lastClaim) > c.MinIdle/2 {
			lastClaim = time.Now()
			c.claimStale(ctx)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (c *Consumer) drainPending(ctx context.Context) {
	for ctx.Err() == nil {
		streams, err := c.St.R.XReadGroup(ctx, &redis.XReadGroupArgs{Group: Group, Consumer: c.Name,
			Streams: []string{c.St.StreamKey(), "0"}, Count: c.BatchSize}).Result()
		if err != nil || len(streams) == 0 || len(streams[0].Messages) == 0 {
			return
		}
		if err := c.process(ctx, streams[0].Messages); err != nil {
			return
		}
	}
}

// claimStale takes over entries another consumer read but never acked.
func (c *Consumer) claimStale(ctx context.Context) {
	start := "0-0"
	for ctx.Err() == nil {
		msgs, next, err := c.St.R.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: c.St.StreamKey(), Group: Group,
			Consumer: c.Name, MinIdle: c.MinIdle, Start: start, Count: c.BatchSize}).Result()
		if err != nil {
			return
		}
		if len(msgs) > 0 {
			if c.process(ctx, msgs) != nil {
				return
			}
		}
		if next == "0-0" || next == "" {
			return
		}
		start = next
	}
}

// ProcessOnce reads and persists one batch (tests).
func (c *Consumer) ProcessOnce(ctx context.Context) (int, error) {
	if c.BatchSize == 0 {
		c.BatchSize = 500
	}
	if err := EnsureGroup(ctx, c.St); err != nil {
		return 0, err
	}
	streams, err := c.St.R.XReadGroup(ctx, &redis.XReadGroupArgs{Group: Group, Consumer: c.Name,
		Streams: []string{c.St.StreamKey(), ">"}, Count: c.BatchSize, Block: -1}).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range streams {
		n += len(s.Messages)
		if err := c.process(ctx, s.Messages); err != nil {
			return n, err
		}
	}
	return n, nil
}

// ProcessMessages persists the given messages (tests: redelivery).
func (c *Consumer) ProcessMessages(ctx context.Context, msgs []redis.XMessage) error {
	return c.process(ctx, msgs)
}

func (c *Consumer) process(ctx context.Context, msgs []redis.XMessage) error {
	entries := make([]entry, 0, len(msgs))
	for _, m := range msgs {
		e := entry{id: m.ID}
		raw, _ := m.Values["r"].(string)
		if err := json.Unmarshal([]byte(raw), &e.rec); err != nil || e.rec.RequestID == uuid.Nil {
			c.Log.Error("drop malformed settlement record", "id", m.ID, "err", err)
			e.bad = true
		}
		entries = append(entries, e)
	}
	batchID := msgs[0].ID + "~" + msgs[len(msgs)-1].ID
	var settled []Summary
	var syncs []rstate.BaseSync
	var ksyncs []rstate.KeySync
	err := c.Store.InTx(ctx, func(tx *store.Tx) error {
		var err error
		settled, syncs, ksyncs, err = persist(ctx, tx, entries, batchID)
		return err
	})
	if err != nil {
		metrics.ConsumerErrors.Inc()
		c.Log.Error("persist settlement batch", "batch", batchID, "n", len(msgs), "err", err)
		return err
	}
	persisted := make([]rstate.Persisted, 0, len(entries))
	for _, e := range entries {
		p := rstate.Persisted{MessageID: e.id}
		if !e.bad && e.rec.Wallet != "" {
			p.Wallet, _ = rstate.ParseWallet(e.rec.Wallet)
			p.Amount = e.rec.Billed
		}
		if !e.bad && e.rec.KeyID != uuid.Nil && e.rec.Billed != 0 {
			p.Key, p.KeyAll = e.rec.KeyID, e.rec.Billed
			if BalanceTier(e.rec.Tier) {
				p.KeyBalance = e.rec.Billed
			}
		}
		persisted = append(persisted, p)
	}
	if err := c.St.MarkPersisted(context.WithoutCancel(ctx), Group, persisted, syncs, ksyncs...); err != nil {
		c.Log.Error("ack settlement batch", "batch", batchID, "err", err)
		return err
	}
	metrics.SettledRecords.Add(float64(len(settled)))
	if c.OnSettled != nil && len(settled) > 0 {
		c.OnSettled(settled)
	}
	return nil
}

type hourKey struct {
	hour    time.Time
	user    uuid.UUID
	key     uuid.UUID
	account uuid.UUID
	model   string
	tier    string
}

type periodKey struct {
	sub   uuid.UUID
	start time.Time
}

type periodAgg struct {
	end      time.Time
	quota    int64
	billed   int64
	upstream int64
}

type keyCharge struct {
	at           time.Time
	all, balance int64
}

func persist(ctx context.Context, tx *store.Tx, entries []entry, batchID string) ([]Summary, []rstate.BaseSync, []rstate.KeySync, error) {
	keyCharges := map[uuid.UUID][]keyCharge{}
	hours := map[hourKey]*sqlc.AddUsageHourlyParams{}
	periods := map[periodKey]*periodAgg{}
	charges := map[uuid.UUID]int64{}
	var chargeOrder []uuid.UUID
	keyLast := map[uuid.UUID]time.Time{}
	var settled []Summary
	var syncs []rstate.BaseSync
	for _, e := range entries {
		if e.bad {
			continue
		}
		r := e.rec
		inserted, err := tx.Q.InsertRequestLog(ctx, sqlc.InsertRequestLogParams{
			RequestID: r.RequestID, CreatedAt: r.CreatedAt, UserID: &r.UserID, KeyID: &r.KeyID, AccountID: r.AccountID,
			Tier: r.Tier, SubscriptionID: r.SubscriptionID, Endpoint: r.Endpoint, ModelRaw: r.ModelRaw, Model: r.Model,
			Stream: r.Stream, StatusCode: int32(r.StatusCode), ErrorType: r.ErrorType, InputTokens: r.InputTokens,
			CacheReadTokens: r.CacheReadTokens, CacheWriteTokens: r.CacheWriteTokens, OutputTokens: r.OutputTokens,
			ReasoningTokens: r.ReasoningTokens, UpstreamCostMicro: r.UpstreamCost, BilledMicro: r.Billed,
			TierMultiplier: r.TierMult, ModelMultiplier: r.ModelMult, Unpriced: r.Unpriced, UsageMissing: r.UsageMissing,
			LatencyMs: int32(r.LatencyMs), FirstByteMs: int32(r.FirstByteMs), Retries: int32(r.Retries),
			SessionHash: r.SessionHash, ClientIp: r.ClientIP, UserAgent: r.UserAgent,
		})
		if err != nil {
			if isNoRows(err) {
				continue // duplicate delivery
			}
			return nil, nil, nil, fmt.Errorf("insert request log %s: %w", r.RequestID, err)
		}
		_ = inserted
		settled = append(settled, r.Summarize())

		account := uuid.Nil
		if r.AccountID != nil {
			account = *r.AccountID
		}
		hk := hourKey{hour: r.CreatedAt.UTC().Truncate(time.Hour), user: r.UserID, key: r.KeyID, account: account, model: r.Model, tier: r.Tier}
		h := hours[hk]
		if h == nil {
			h = &sqlc.AddUsageHourlyParams{Hour: hk.hour, UserID: hk.user, KeyID: hk.key, AccountID: hk.account, Model: hk.model, Tier: hk.tier}
			hours[hk] = h
		}
		h.Requests++
		if r.StatusCode >= 400 {
			h.Errors++
		}
		h.InputTokens += r.InputTokens
		h.CacheReadTokens += r.CacheReadTokens
		h.CacheWriteTokens += r.CacheWriteTokens
		h.OutputTokens += r.OutputTokens
		h.ReasoningTokens += r.ReasoningTokens
		h.UpstreamCostMicro += r.UpstreamCost
		h.BilledMicro += r.Billed

		if r.Tier == "plan" && r.SubscriptionID != nil && r.PeriodStart != nil && r.PeriodEnd != nil {
			pk := periodKey{sub: *r.SubscriptionID, start: r.PeriodStart.UTC()}
			p := periods[pk]
			if p == nil {
				p = &periodAgg{end: r.PeriodEnd.UTC(), quota: r.PeriodQuota}
				periods[pk] = p
			}
			p.billed += r.Billed
			p.upstream += r.UpstreamCost
		}
		if BalanceTier(r.Tier) && r.Billed != 0 {
			if _, ok := charges[r.UserID]; !ok {
				chargeOrder = append(chargeOrder, r.UserID)
			}
			charges[r.UserID] += r.Billed
		}
		if r.KeyID != uuid.Nil && r.Billed != 0 {
			kc := keyCharge{at: r.CreatedAt, all: r.Billed}
			if BalanceTier(r.Tier) {
				kc.balance = r.Billed
			}
			keyCharges[r.KeyID] = append(keyCharges[r.KeyID], kc)
		}
		if r.KeyID != uuid.Nil && r.CreatedAt.After(keyLast[r.KeyID]) {
			keyLast[r.KeyID] = r.CreatedAt
		}
	}
	for _, h := range hours {
		if err := tx.Q.AddUsageHourly(ctx, *h); err != nil {
			return nil, nil, nil, fmt.Errorf("add usage hourly: %w", err)
		}
	}
	for pk, p := range periods {
		row, err := tx.Q.AddPeriodUsage(ctx, sqlc.AddPeriodUsageParams{SubscriptionID: pk.sub, PeriodStart: pk.start, PeriodEnd: p.end,
			QuotaMicro: p.quota, UsedBilledMicro: p.billed, UsedUpstreamMicro: p.upstream})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("add period usage: %w", err)
		}
		syncs = append(syncs, rstate.BaseSync{Wallet: rstate.PeriodWallet(pk.sub, pk.start), Base: row.Base, Version: row.Version})
	}
	for _, user := range chargeOrder {
		amount := charges[user]
		entry, inserted, version, err := tx.AppendLedgerVersion(ctx, store.LedgerParams{UserID: user, Type: "api_charge", Amount: -amount,
			RefType: "settle_batch", RefID: batchID, IdempotencyKey: "api_charge:" + batchID + ":" + user.String()})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("append api charge: %w", err)
		}
		if inserted {
			syncs = append(syncs, rstate.BaseSync{Wallet: rstate.UserWallet(user), Base: entry.BalanceAfterMicro, Version: version})
		}
	}
	if len(keyLast) > 0 {
		var latest time.Time
		ids := make([]uuid.UUID, 0, len(keyLast))
		for id, t := range keyLast {
			ids = append(ids, id)
			if t.After(latest) {
				latest = t
			}
		}
		if err := tx.Q.TouchAPIKeys(ctx, sqlc.TouchAPIKeysParams{Column1: ids, Column2: latest}); err != nil {
			return nil, nil, nil, fmt.Errorf("touch keys: %w", err)
		}
	}
	ksyncs, err := persistKeyUsage(ctx, tx, keyCharges)
	if err != nil {
		return nil, nil, nil, err
	}
	return settled, syncs, ksyncs, nil
}

// persistKeyUsage adds the batch's charges to the key meters in PostgreSQL,
// in key order (no deadlock between consumers), counting only requests that
// started at or after the key's last reset.
func persistKeyUsage(ctx context.Context, tx *store.Tx, charges map[uuid.UUID][]keyCharge) ([]rstate.KeySync, error) {
	ids := make([]uuid.UUID, 0, len(charges))
	for id := range charges {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	var out []rstate.KeySync
	for _, id := range ids {
		resetAt, err := tx.Q.LockAPIKeyUsage(ctx, id)
		if isNoRows(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("lock key usage: %w", err)
		}
		var all, bal int64
		for _, c := range charges[id] {
			if !c.at.Before(resetAt) {
				all += c.all
				bal += c.balance
			}
		}
		row, err := tx.Q.AddAPIKeyUsage(ctx, sqlc.AddAPIKeyUsageParams{ID: id, UsedAllMicro: all, UsedBalanceMicro: bal})
		if err != nil {
			return nil, fmt.Errorf("add key usage: %w", err)
		}
		out = append(out, rstate.KeySync{Key: id, All: row.UsedAllMicro, Balance: row.UsedBalanceMicro, Version: row.UsageVersion})
	}
	return out, nil
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
