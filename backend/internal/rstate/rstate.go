// Package rstate keeps the shared real-time state in Redis: available
// balances and plan period quotas with their holds, concurrency leases, RPM
// windows, session affinity, per-account spend buckets, cooldowns, quota
// snapshots and the settlement stream. Every mutation that must be atomic is
// a Lua script.
//
// Wallets. A wallet is either a user's balance ("u:<user>") or a plan
// subscription's period quota ("s:<sub>:<period start unix>"). For a wallet W:
//
//	wb:W  HASH {b, v}: the value committed in PostgreSQL (balance, or period
//	      quota − used) and its version (users.balance_version /
//	      plan_usage_periods.version)
//	wp:W  amount settled in Redis whose stream entry is not in PG yet
//	wh:W  ZSET of live holds: member request id, score expiry (unix ms)
//	wa:W  HASH request id -> held amount
//
// available = b − pending − Σ live holds.
//
// b is only ever replaced by a value with a higher version (SyncBase), and
// pending is decremented exactly once per stream entry after its PG commit,
// so the outbox (admin changes), the consumer (charges) and a rebuild after
// key loss commute: whatever order they land in, the wallet converges to
// PG − (entries not yet persisted). Holds expire on their own: every script
// that reads a wallet first drops holds whose score is in the past. These
// keys must never be evicted (Redis runs with maxmemory-policy noeviction).
package rstate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// State wraps the Redis client with the bridge key layout.
type State struct {
	R      redis.UniversalClient
	Prefix string
}

// New returns a State whose keys start with prefix (e.g. "ob:bridge:").
func New(r redis.UniversalClient, prefix string) *State {
	return &State{R: r, Prefix: prefix}
}

// Key names.
func (s *State) k(parts ...string) string {
	out := s.Prefix
	for i, p := range parts {
		if i > 0 {
			out += ":"
		}
		out += p
	}
	return out
}

// Wallet identifies a balance or a plan period quota.
type Wallet string

// UserWallet is the balance wallet of a user.
func UserWallet(user uuid.UUID) Wallet { return Wallet("u:" + user.String()) }

// PeriodWallet is the quota wallet of a subscription period.
func PeriodWallet(sub uuid.UUID, periodStart time.Time) Wallet {
	return Wallet("s:" + sub.String() + ":" + strconv.FormatInt(periodStart.Unix(), 10))
}

func (s *State) walletKeys(w Wallet) []string {
	return []string{s.k("wb", string(w)), s.k("wh", string(w)), s.k("wa", string(w)), s.k("wp", string(w))}
}

// Channel names.
func (s *State) ChanInvalidate() string { return s.k("chan", "inval") }
func (s *State) ChanEvents() string     { return s.k("chan", "events") }
func (s *State) ChanSettings() string   { return s.k("chan", "settings") }
func (s *State) ChanRouting() string    { return s.k("chan", "routing") }

// StreamKey is the settlement stream.
func (s *State) StreamKey() string { return s.k("settle") }

// ErrWalletMissing reports that a wallet's committed base is not in Redis
// and must be synced from PostgreSQL.
var ErrWalletMissing = errors.New("wallet missing in redis")

// pruneLua drops expired holds of a wallet and returns the live hold sum.
const pruneLua = `
local function prune(wh, wa, now)
  local expired = redis.call('ZRANGEBYSCORE', wh, '-inf', now)
  for _, m in ipairs(expired) do redis.call('HDEL', wa, m) end
  if #expired > 0 then redis.call('ZREMRANGEBYSCORE', wh, '-inf', now) end
  local sum = 0
  local vals = redis.call('HVALS', wa)
  for _, v in ipairs(vals) do sum = sum + tonumber(v) end
  return sum
end
`

var holdScript = redis.NewScript(pruneLua + `
-- KEYS: wb, wh, wa, wp ; ARGV: now_ms, ttl_ms, request_id, want
local b = redis.call('HGET', KEYS[1], 'b')
if not b then return {-1, 0, 0} end
local now = tonumber(ARGV[1])
local held = prune(KEYS[2], KEYS[3], now)
local avail = tonumber(b) - tonumber(redis.call('GET', KEYS[4]) or '0') - held
if avail <= 0 then return {0, 0, avail} end
local want = tonumber(ARGV[4])
if want > avail then want = avail end
if want < 0 then want = 0 end
redis.call('ZADD', KEYS[2], now + tonumber(ARGV[2]), ARGV[3])
redis.call('HSET', KEYS[3], ARGV[3], want)
redis.call('PEXPIRE', KEYS[2], 86400000)
redis.call('PEXPIRE', KEYS[3], 86400000)
return {1, want, avail}
`)

// HoldResult is the outcome of a hold attempt.
type HoldResult struct {
	OK        bool
	Held      int64
	Available int64
}

// Hold reserves up to want from a wallet when its available amount is
// positive. The hold is capped at the available amount, so a wallet only goes
// negative when the final charge exceeds its hold.
func (s *State) Hold(ctx context.Context, w Wallet, requestID string, want int64, ttl time.Duration, now time.Time) (HoldResult, error) {
	keys := s.walletKeys(w)
	res, err := holdScript.Run(ctx, s.R, keys, now.UnixMilli(), ttl.Milliseconds(), requestID, want).Int64Slice()
	if err != nil {
		return HoldResult{}, err
	}
	if res[0] == -1 {
		return HoldResult{}, ErrWalletMissing
	}
	return HoldResult{OK: res[0] == 1, Held: res[1], Available: res[2]}, nil
}

var availableScript = redis.NewScript(pruneLua + `
-- KEYS: wb, wh, wa, wp
local b = redis.call('HGET', KEYS[1], 'b')
if not b then return {-1, 0} end
local pending = tonumber(redis.call('GET', KEYS[4]) or '0')
return {1, tonumber(b) - pending - prune(KEYS[2], KEYS[3], tonumber(ARGV[1]))}
`)

// Available returns base − pending − live holds.
func (s *State) Available(ctx context.Context, w Wallet, now time.Time) (int64, error) {
	keys := s.walletKeys(w)
	res, err := availableScript.Run(ctx, s.R, keys, now.UnixMilli()).Int64Slice()
	if err != nil {
		return 0, err
	}
	if res[0] == -1 {
		return 0, ErrWalletMissing
	}
	return res[1], nil
}

// AvailableMany returns the available amounts of several wallets; missing
// wallets are absent from the map.
func (s *State) AvailableMany(ctx context.Context, ws []Wallet, now time.Time) (map[Wallet]int64, error) {
	out := make(map[Wallet]int64, len(ws))
	if len(ws) == 0 {
		return out, nil
	}
	pipe := s.R.Pipeline()
	cmds := make([]*redis.Cmd, len(ws))
	for i, w := range ws {
		keys := s.walletKeys(w)
		cmds[i] = availableScript.Run(ctx, pipe, keys, now.UnixMilli())
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		// NOSCRIPT inside a pipeline: load and retry once.
		if err := availableScript.Load(ctx, s.R).Err(); err != nil {
			return nil, err
		}
		pipe = s.R.Pipeline()
		for i, w := range ws {
			keys := s.walletKeys(w)
			cmds[i] = availableScript.Run(ctx, pipe, keys, now.UnixMilli())
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, err
		}
	}
	for i, w := range ws {
		res, err := cmds[i].Int64Slice()
		if err != nil {
			return nil, err
		}
		if res[0] == 1 {
			out[w] = res[1]
		}
	}
	return out, nil
}

// ExtendHold pushes a live hold's expiry out (long streams).
func (s *State) ExtendHold(ctx context.Context, w Wallet, requestID string, ttl time.Duration, now time.Time) error {
	keys := s.walletKeys(w)
	return s.R.ZAddXX(ctx, keys[1], redis.Z{Score: float64(now.Add(ttl).UnixMilli()), Member: requestID}).Err()
}

var releaseScript = redis.NewScript(`
redis.call('ZREM', KEYS[1], ARGV[1])
redis.call('HDEL', KEYS[2], ARGV[1])
return 1
`)

// ReleaseHold drops a hold without charging.
func (s *State) ReleaseHold(ctx context.Context, w Wallet, requestID string) error {
	keys := s.walletKeys(w)
	return releaseScript.Run(ctx, s.R, keys[1:3], requestID).Err()
}

var syncScript = redis.NewScript(`
-- KEYS: wb ; ARGV: base, version
local cur = redis.call('HGET', KEYS[1], 'v')
if cur and tonumber(cur) >= tonumber(ARGV[2]) then return 0 end
redis.call('HSET', KEYS[1], 'b', ARGV[1], 'v', ARGV[2])
return 1
`)

// SyncBase installs a committed PostgreSQL value unless Redis already holds
// the same or a newer version. It serves rebuilds, outbox deliveries and
// consumer commits alike.
func (s *State) SyncBase(ctx context.Context, w Wallet, base, version int64) error {
	return syncScript.Run(ctx, s.R, []string{s.k("wb", string(w))}, base, version).Err()
}

// DropWallet deletes a wallet's committed base (tests and repair).
func (s *State) DropWallet(ctx context.Context, w Wallet) error {
	return s.R.Del(ctx, s.k("wb", string(w))).Err()
}

// Base returns the committed base and version of a wallet.
func (s *State) Base(ctx context.Context, w Wallet) (base, version int64, ok bool, err error) {
	vals, err := s.R.HMGet(ctx, s.k("wb", string(w)), "b", "v").Result()
	if err != nil {
		return 0, 0, false, err
	}
	bs, ok1 := vals[0].(string)
	vs, ok2 := vals[1].(string)
	if !ok1 || !ok2 {
		return 0, 0, false, nil
	}
	base, _ = strconv.ParseInt(bs, 10, 64)
	version, _ = strconv.ParseInt(vs, 10, 64)
	return base, version, true, nil
}

// Pending returns the not-yet-persisted settled amount of a wallet.
func (s *State) Pending(ctx context.Context, w Wallet) (int64, error) {
	v, err := s.R.Get(ctx, s.k("wp", string(w))).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return v, err
}

// ---- concurrency and RPM -------------------------------------------------

// Limit is one concurrency or RPM dimension.
type Limit struct {
	Key   string
	Limit int64 // <= 0 or unlimited is not passed
}

// ConcKey / RPMKey name the per-dimension sets.
func (s *State) ConcKey(kind string, id uuid.UUID) string { return s.k("c", kind, id.String()) }
func (s *State) RPMKey(kind string, id uuid.UUID) string  { return s.k("r", kind, id.String()) }

var admitScript = redis.NewScript(`
-- KEYS: nconc concurrency sets, then rpm sets
-- ARGV: now_ms, lease_ttl_ms, request_id, nconc, limits...
local now = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local id = ARGV[3]
local nconc = tonumber(ARGV[4])
for i, key in ipairs(KEYS) do
  local limit = tonumber(ARGV[4 + i])
  if i <= nconc then
    redis.call('ZREMRANGEBYSCORE', key, '-inf', now)
    if redis.call('ZCARD', key) >= limit then return {0, i} end
  else
    redis.call('ZREMRANGEBYSCORE', key, '-inf', now - 60000)
    if redis.call('ZCARD', key) >= limit then return {0, i} end
  end
end
for i, key in ipairs(KEYS) do
  if i <= nconc then
    redis.call('ZADD', key, now + ttl, id)
    redis.call('PEXPIRE', key, ttl * 2)
  else
    redis.call('ZADD', key, now, id)
    redis.call('PEXPIRE', key, 120000)
  end
end
return {1, 0}
`)

// Admit atomically checks every concurrency and RPM limit and, when all pass,
// records the request in each. It returns the index of the failing limit
// (into conc followed by rpm) when one is full.
func (s *State) Admit(ctx context.Context, requestID string, conc, rpm []Limit, leaseTTL time.Duration, now time.Time) (bool, int, error) {
	if len(conc)+len(rpm) == 0 {
		return true, -1, nil
	}
	keys := make([]string, 0, len(conc)+len(rpm))
	args := []any{now.UnixMilli(), leaseTTL.Milliseconds(), requestID, len(conc)}
	for _, l := range conc {
		keys = append(keys, l.Key)
		args = append(args, l.Limit)
	}
	for _, l := range rpm {
		keys = append(keys, l.Key)
		args = append(args, l.Limit)
	}
	res, err := admitScript.Run(ctx, s.R, keys, args...).Int64Slice()
	if err != nil {
		return false, -1, err
	}
	return res[0] == 1, int(res[1]) - 1, nil
}

// ExtendLeases refreshes the expiry of a request's concurrency leases.
func (s *State) ExtendLeases(ctx context.Context, requestID string, keys []string, ttl time.Duration, now time.Time) {
	if len(keys) == 0 {
		return
	}
	pipe := s.R.Pipeline()
	for _, key := range keys {
		pipe.ZAddXX(ctx, key, redis.Z{Score: float64(now.Add(ttl).UnixMilli()), Member: requestID})
		pipe.PExpire(ctx, key, 2*ttl)
	}
	_, _ = pipe.Exec(ctx)
}

// ReleaseLeases removes a request from concurrency sets.
func (s *State) ReleaseLeases(ctx context.Context, requestID string, keys []string) {
	if len(keys) == 0 {
		return
	}
	pipe := s.R.Pipeline()
	for _, key := range keys {
		pipe.ZRem(ctx, key, requestID)
	}
	_, _ = pipe.Exec(ctx)
}

var acquireOneScript = redis.NewScript(`
local now = tonumber(ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
if tonumber(ARGV[4]) > 0 and redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[4]) then return 0 end
redis.call('ZADD', KEYS[1], now + tonumber(ARGV[2]), ARGV[3])
redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[2]) * 2)
return 1
`)

// AcquireLease takes one lease of key when fewer than limit are live; a
// limit <= 0 means unlimited (the lease is still recorded for display).
func (s *State) AcquireLease(ctx context.Context, key, requestID string, limit int64, ttl time.Duration, now time.Time) (bool, error) {
	n, err := acquireOneScript.Run(ctx, s.R, []string{key}, now.UnixMilli(), ttl.Milliseconds(), requestID, limit).Int64()
	return n == 1, err
}

// LiveLeases counts live leases of key.
func (s *State) LiveLeases(ctx context.Context, key string, now time.Time) int64 {
	n, _ := s.R.ZCount(ctx, key, strconv.FormatInt(now.UnixMilli(), 10), "+inf").Result()
	return n
}

// ---- affinity, cooldown, spend --------------------------------------------

// AffinityKey names a session's sticky account mapping.
func (s *State) AffinityKey(sessionHash string) string { return s.k("aff", sessionHash) }

// GetAffinity returns the sticky account of a session, if any.
func (s *State) GetAffinity(ctx context.Context, sessionHash string) (uuid.UUID, bool) {
	v, err := s.R.Get(ctx, s.AffinityKey(sessionHash)).Result()
	if err != nil {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(v)
	return id, err == nil
}

// SetAffinity maps a session to an account with a sliding TTL.
func (s *State) SetAffinity(ctx context.Context, sessionHash string, account uuid.UUID, ttl time.Duration) error {
	return s.R.Set(ctx, s.AffinityKey(sessionHash), account.String(), ttl).Err()
}

// CooldownKey names an account's cooldown marker.
func (s *State) CooldownKey(account uuid.UUID) string { return s.k("cd", account.String()) }

// SetCooldown puts an account in cooldown until the given time; an existing
// longer cooldown is kept.
func (s *State) SetCooldown(ctx context.Context, account uuid.UUID, until time.Time, reason string, now time.Time) error {
	ttl := until.Sub(now)
	if ttl <= 0 {
		return nil
	}
	key := s.CooldownKey(account)
	if cur, ok := s.Cooldown(ctx, account); ok && cur.Until.After(until) {
		return nil
	}
	return s.R.Set(ctx, key, strconv.FormatInt(until.UnixMilli(), 10)+"|"+reason, ttl).Err()
}

// CooldownInfo is an active cooldown.
type CooldownInfo struct {
	Until  time.Time
	Reason string
}

// Cooldown returns an account's active cooldown.
func (s *State) Cooldown(ctx context.Context, account uuid.UUID) (CooldownInfo, bool) {
	v, err := s.R.Get(ctx, s.CooldownKey(account)).Result()
	if err != nil {
		return CooldownInfo{}, false
	}
	return parseCooldown(v)
}

func parseCooldown(v string) (CooldownInfo, bool) {
	ms, reason := v, ""
	for i := 0; i < len(v); i++ {
		if v[i] == '|' {
			ms, reason = v[:i], v[i+1:]
			break
		}
	}
	n, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return CooldownInfo{}, false
	}
	return CooldownInfo{Until: time.UnixMilli(n).UTC(), Reason: reason}, true
}

// Cooldowns returns the active cooldowns of several accounts.
func (s *State) Cooldowns(ctx context.Context, accounts []uuid.UUID) map[uuid.UUID]CooldownInfo {
	out := map[uuid.UUID]CooldownInfo{}
	if len(accounts) == 0 {
		return out
	}
	keys := make([]string, len(accounts))
	for i, a := range accounts {
		keys[i] = s.CooldownKey(a)
	}
	vals, err := s.R.MGet(ctx, keys...).Result()
	if err != nil {
		return out
	}
	for i, v := range vals {
		if str, ok := v.(string); ok {
			if info, ok := parseCooldown(str); ok {
				out[accounts[i]] = info
			}
		}
	}
	return out
}

// ClearCooldown removes an account's cooldown.
func (s *State) ClearCooldown(ctx context.Context, account uuid.UUID) error {
	return s.R.Del(ctx, s.CooldownKey(account)).Err()
}

// spendKey names an account's spend buckets of one hour (fields: minute).
func (s *State) spendKey(account uuid.UUID, hour int64) string {
	return s.k("sp", account.String(), strconv.FormatInt(hour, 10))
}

// SpendRetention is how long minute buckets are kept.
const SpendRetention = 8 * 24 * time.Hour

// SpendSince sums an account's upstream cost recorded in minute buckets from
// the minute containing since through now.
func (s *State) SpendSince(ctx context.Context, account uuid.UUID, since, now time.Time) (int64, error) {
	if since.After(now) {
		return 0, nil
	}
	if now.Sub(since) > SpendRetention {
		since = now.Add(-SpendRetention)
	}
	fromMin := since.Unix() / 60
	pipe := s.R.Pipeline()
	var cmds []*redis.MapStringStringCmd
	for h := fromMin / 60; h <= now.Unix()/3600; h++ {
		cmds = append(cmds, pipe.HGetAll(ctx, s.spendKey(account, h)))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return 0, err
	}
	var total int64
	for _, c := range cmds {
		for field, val := range c.Val() {
			minute, err := strconv.ParseInt(field, 10, 64)
			if err != nil || minute < fromMin {
				continue
			}
			v, _ := strconv.ParseInt(val, 10, 64)
			total += v
		}
	}
	return total, nil
}

// AddSpend records upstream cost in an account's minute bucket (used outside
// settlement, e.g. tests).
func (s *State) AddSpend(ctx context.Context, account uuid.UUID, at time.Time, micro int64) error {
	key := s.spendKey(account, at.Unix()/3600)
	pipe := s.R.Pipeline()
	pipe.HIncrBy(ctx, key, strconv.FormatInt(at.Unix()/60, 10), micro)
	pipe.Expire(ctx, key, SpendRetention+time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

// ---- snapshots and caches -------------------------------------------------

// QuotaKey / ModelsKey / PeriodKey name per-account and per-subscription values.
func (s *State) QuotaKey(account uuid.UUID) string  { return s.k("q", account.String()) }
func (s *State) ModelsKey(account uuid.UUID) string { return s.k("m", account.String()) }
func (s *State) PeriodKey(sub uuid.UUID) string     { return s.k("per", sub.String()) }
func (s *State) AuthKey(hashHex string) string      { return s.k("auth", hashHex) }
func (s *State) SessionKey(token string) string     { return s.k("sess", token) }
func (s *State) LoginLimitKey(id string) string     { return s.k("ll", id) }
func (s *State) LeaderKey() string                  { return s.k("leader") }
func (s *State) PriceKey() string                   { return s.k("prices") }
func (s *State) ReconcileKey() string               { return s.k("reconcile") }

// Key exposes the key builder for packages with their own entries.
func (s *State) Key(parts ...string) string { return s.k(parts...) }

// ---- settlement -----------------------------------------------------------

// SettleOp is the Redis side of a settlement.
type SettleOp struct {
	RequestID string
	// Wallet charged (empty for unbilled requests). Hold is released first.
	Wallet      Wallet
	HasHold     bool
	Charge      int64
	Account     uuid.UUID
	AccountCost int64
	At          time.Time
	// Affinity renewal; empty session hash skips it.
	SessionHash string
	AffinityTTL time.Duration
	// Record is the JSON settlement record appended to the stream.
	Record []byte
	// Key, when set, has its hold released and Charge added to its meter
	// (scope "a", and "b" too when KeyBalance: a balance tier charged).
	Key        uuid.UUID
	KeyBalance bool
}

var settleScript = redis.NewScript(`
-- KEYS: wb, wh, wa, wp, spend, aff, stream, kp, kh, khv, kwh:a, kwh:b, kwr
-- ARGV: request_id, charge, has_wallet, minute, cost, account, aff_ttl_ms, record, spend_ttl_s,
--       has_key, key_balance, kwm_a_prefix, kwm_b_prefix, key_ttl_s
local id = ARGV[1]
redis.call('ZREM', KEYS[2], id)
redis.call('HDEL', KEYS[3], id)
local charge = tonumber(ARGV[2])
if ARGV[3] == '1' and charge ~= 0 then
  redis.call('INCRBY', KEYS[4], charge)
end
local cost = tonumber(ARGV[5])
if cost ~= 0 and ARGV[6] ~= '' then
  redis.call('HINCRBY', KEYS[5], ARGV[4], cost)
  redis.call('EXPIRE', KEYS[5], tonumber(ARGV[9]))
end
if ARGV[6] ~= '' and tonumber(ARGV[7]) > 0 then
  redis.call('SET', KEYS[6], ARGV[6], 'PX', tonumber(ARGV[7]))
end
if ARGV[10] == '1' then
  redis.call('ZREM', KEYS[9], id)
  redis.call('HDEL', KEYS[10], id)
  if charge ~= 0 then
    local minute = ARGV[4]
    local hour = tostring(math.floor(tonumber(minute) / 60))
    local ttl = tonumber(ARGV[14])
    redis.call('HINCRBY', KEYS[8], 'a', ARGV[2])
    redis.call('HINCRBY', KEYS[11], hour, ARGV[2])
    redis.call('EXPIRE', KEYS[11], ttl)
    redis.call('HINCRBY', ARGV[12] .. hour, minute, ARGV[2])
    redis.call('EXPIRE', ARGV[12] .. hour, ttl)
    if ARGV[11] == '1' then
      redis.call('HINCRBY', KEYS[8], 'b', ARGV[2])
      redis.call('HINCRBY', KEYS[12], hour, ARGV[2])
      redis.call('EXPIRE', KEYS[12], ttl)
      redis.call('HINCRBY', ARGV[13] .. hour, minute, ARGV[2])
      redis.call('EXPIRE', ARGV[13] .. hour, ttl)
    end
    redis.call('EXPIRE', KEYS[13], ttl)
  end
end
return redis.call('XADD', KEYS[7], '*', 'r', ARGV[8])
`)

// Settle releases the wallet and key holds, charges the wallet and the key
// meter (pending and window buckets), records account spend, renews affinity
// and appends the settlement record to the stream, atomically.
func (s *State) Settle(ctx context.Context, op SettleOp) (string, error) {
	w := op.Wallet
	if w == "" {
		w = "none"
	}
	keys := s.walletKeys(w)
	spend, aff, account := s.k("sp", "none"), s.k("aff", "none"), ""
	if op.Account != uuid.Nil {
		account = op.Account.String()
		spend = s.spendKey(op.Account, op.At.Unix()/3600)
	}
	affTTL := int64(0)
	if op.SessionHash != "" && account != "" {
		aff = s.AffinityKey(op.SessionHash)
		affTTL = op.AffinityTTL.Milliseconds()
	}
	hasWallet := "0"
	if op.Wallet != "" {
		hasWallet = "1"
	}
	none := s.k("none")
	kp, kh, khv, kwa, kwb, kwr, pa, pb := none, none, none, none, none, none, "", ""
	hasKey, keyBal := "0", "0"
	if op.Key != uuid.Nil {
		_, kp, kh, khv, kwr = s.keyKeys(op.Key)
		kwa, kwb = s.keyHourKey(op.Key, "a"), s.keyHourKey(op.Key, "b")
		pa, pb = s.keyMinutePrefix(op.Key, "a"), s.keyMinutePrefix(op.Key, "b")
		hasKey = "1"
		if op.KeyBalance {
			keyBal = "1"
		}
	}
	return settleScript.Run(ctx, s.R, []string{keys[0], keys[1], keys[2], keys[3], spend, aff, s.StreamKey(), kp, kh, khv, kwa, kwb, kwr},
		op.RequestID, op.Charge, hasWallet, strconv.FormatInt(op.At.Unix()/60, 10), op.AccountCost, account, affTTL,
		op.Record, int64((SpendRetention + time.Hour).Seconds()), hasKey, keyBal, pa, pb, int64(KeyBucketRetention.Seconds())).Text()
}

// Persisted is one stream entry whose PG transaction committed.
type Persisted struct {
	MessageID string
	Wallet    Wallet
	Amount    int64
	// Key meter pending amounts to decrement with the same exactly-once guard.
	Key        uuid.UUID
	KeyAll     int64
	KeyBalance int64
}

// KeySync is a committed (all, balance, version) of a key meter.
type KeySync struct {
	Key          uuid.UUID
	All, Balance int64
	Version      int64
}

// BaseSync is a committed (base, version) of a wallet.
type BaseSync struct {
	Wallet  Wallet
	Base    int64
	Version int64
}

var persistedScript = redis.NewScript(`
-- KEYS: stream ; ARGV: group, n, nsync, nksync, then per sync: base_key, base, version
-- then per key sync: kb_key, all, balance, version
-- then per entry: msg_id, marker_key, pending_key, amount, kp_key, kall, kbal
local n = tonumber(ARGV[2])
local nsync = tonumber(ARGV[3])
local nksync = tonumber(ARGV[4])
local base = 5
for i = 1, nsync do
  local key, b, v = ARGV[base], ARGV[base + 1], ARGV[base + 2]
  local cur = redis.call('HGET', key, 'v')
  if (not cur) or tonumber(cur) < tonumber(v) then redis.call('HSET', key, 'b', b, 'v', v) end
  base = base + 3
end
for i = 1, nksync do
  local key, a, b, v = ARGV[base], ARGV[base + 1], ARGV[base + 2], ARGV[base + 3]
  local cur = redis.call('HGET', key, 'v')
  if (not cur) or tonumber(cur) < tonumber(v) then redis.call('HSET', key, 'a', a, 'b', b, 'v', v) end
  base = base + 4
end
for i = 1, n do
  local msg = ARGV[base]
  if (ARGV[base + 2] ~= '' or ARGV[base + 4] ~= '') and redis.call('SET', ARGV[base + 1], '1', 'NX', 'EX', 172800) then
    if ARGV[base + 2] ~= '' then redis.call('DECRBY', ARGV[base + 2], tonumber(ARGV[base + 3])) end
    if ARGV[base + 4] ~= '' then
      redis.call('HINCRBY', ARGV[base + 4], 'a', ARGV[base + 5])
      redis.call('HINCRBY', ARGV[base + 4], 'b', ARGV[base + 6])
    end
  end
  redis.call('XACK', KEYS[1], ARGV[1], msg)
  base = base + 7
end
return n
`)

// MarkPersisted installs the committed wallet and key bases, decrements the
// pending amounts of committed entries exactly once (per message id) and
// acknowledges them, atomically.
func (s *State) MarkPersisted(ctx context.Context, group string, entries []Persisted, syncs []BaseSync, ksyncs ...KeySync) error {
	if len(entries) == 0 && len(syncs) == 0 && len(ksyncs) == 0 {
		return nil
	}
	args := []any{group, len(entries), len(syncs), len(ksyncs)}
	for _, b := range syncs {
		args = append(args, s.k("wb", string(b.Wallet)), b.Base, b.Version)
	}
	for _, k := range ksyncs {
		kb, _, _, _, _ := s.keyKeys(k.Key)
		args = append(args, kb, k.All, k.Balance, k.Version)
	}
	for _, e := range entries {
		pending := ""
		if e.Wallet != "" && e.Amount != 0 {
			pending = s.k("wp", string(e.Wallet))
		}
		kp := ""
		if e.Key != uuid.Nil && (e.KeyAll != 0 || e.KeyBalance != 0) {
			_, kp, _, _, _ = s.keyKeys(e.Key)
		}
		// Key pending deltas are passed negated (strings, never Lua floats).
		args = append(args, e.MessageID, s.k("pd", e.MessageID), pending, e.Amount, kp, -e.KeyAll, -e.KeyBalance)
	}
	// Scripts touch keys passed as arguments; the bridge requires a single
	// (non-cluster) Redis, see README.
	return persistedScript.Run(ctx, s.R, []string{s.StreamKey()}, args...).Err()
}

// String renders a wallet for logs.
func (w Wallet) String() string { return string(w) }

// ParseWallet validates a wallet string from a settlement record.
func ParseWallet(v string) (Wallet, error) {
	if len(v) > 2 && (v[:2] == "u:" || v[:2] == "s:") {
		return Wallet(v), nil
	}
	if v == "" {
		return "", nil
	}
	return "", fmt.Errorf("bad wallet %q", v)
}

// SpendSinceMany is SpendSince for several accounts in one round trip.
func (s *State) SpendSinceMany(ctx context.Context, since map[uuid.UUID]time.Time, now time.Time) (map[uuid.UUID]int64, error) {
	type q struct {
		account uuid.UUID
		fromMin int64
		cmds    []*redis.MapStringStringCmd
	}
	pipe := s.R.Pipeline()
	qs := make([]*q, 0, len(since))
	for account, from := range since {
		if from.After(now) {
			from = now
		}
		if now.Sub(from) > SpendRetention {
			from = now.Add(-SpendRetention)
		}
		item := &q{account: account, fromMin: from.Unix() / 60}
		for h := item.fromMin / 60; h <= now.Unix()/3600; h++ {
			item.cmds = append(item.cmds, pipe.HGetAll(ctx, s.spendKey(account, h)))
		}
		qs = append(qs, item)
	}
	out := make(map[uuid.UUID]int64, len(since))
	if len(qs) == 0 {
		return out, nil
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	for _, item := range qs {
		var total int64
		for _, c := range item.cmds {
			for field, val := range c.Val() {
				minute, err := strconv.ParseInt(field, 10, 64)
				if err != nil || minute < item.fromMin {
					continue
				}
				v, _ := strconv.ParseInt(val, 10, 64)
				total += v
			}
		}
		out[item.account] = total
	}
	return out, nil
}
