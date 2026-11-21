package rstate

// Key meters. Every API key has two usage counters since its last reset —
// "a" (every billed charge) and "b" (charges of the balance tiers only) —
// kept like a wallet: the committed PostgreSQL value with its version plus
// the amount settled in Redis whose stream entry is not persisted yet. The
// same charges are also added to per-minute buckets that serve the sliding
// 5h / 1d / 7d windows. For key K:
//
//	kb:K          HASH {a, b, v}  committed api_keys.used_*_micro / usage_version
//	kp:K          HASH {a, b}     settled, not yet persisted
//	kh:K          ZSET live holds (request id -> expiry ms)
//	khv:K         HASH request id -> held amount (holds of tiers counted under the key's scope)
//	kwh:K:S       HASH hour -> billed of that hour (scope S = a | b)
//	kwm:K:S:H     HASH minute -> billed (hour H), expire after 8 days
//	kwr:K         marker: the window buckets are complete (rebuilt from request_logs when missing)
//
// used(S) = kb.S + kp.S; held = Σ khv; window(S, N) = Σ buckets of the last N
// minutes. Lua builds bucket key names from KEYS[...] prefixes, which (like
// MarkPersisted) requires a single, non-cluster Redis.

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Key window spans.
const (
	Window5h = 5 * 60
	Window1d = 24 * 60
	Window7d = 7 * 24 * 60
	// KeyBucketRetention keeps minute buckets a bit longer than the 7-day window.
	KeyBucketRetention = 8 * 24 * time.Hour
)

// WindowSpans are the window names and spans in minutes, in display order.
var WindowSpans = []struct {
	Name    string
	Minutes int64
}{{"5h", Window5h}, {"1d", Window1d}, {"7d", Window7d}}

// ErrKeyBaseMissing reports that a key meter's committed base is not in Redis.
var ErrKeyBaseMissing = errors.New("key meter base missing in redis")

// ErrKeyWindowsMissing reports that the window buckets must be rebuilt.
var ErrKeyWindowsMissing = errors.New("key windows missing in redis")

func (s *State) keyKeys(key uuid.UUID) (kb, kp, kh, khv, kwr string) {
	id := key.String()
	return s.k("kb", id), s.k("kp", id), s.k("kh", id), s.k("khv", id), s.k("kwr", id)
}

func (s *State) keyHourKey(key uuid.UUID, scope string) string {
	return s.k("kwh", key.String(), scope)
}

// keyMinutePrefix is the prefix of the minute bucket hashes (hour appended).
func (s *State) keyMinutePrefix(key uuid.UUID, scope string) string {
	return s.k("kwm", key.String(), scope) + ":"
}

// KeyLimits are the limits enforced by a hold. A negative value is unlimited.
type KeyLimits struct {
	Scope   string // "a" or "b"
	Quota   int64
	Windows [3]int64 // 5h, 1d, 7d
}

// Any reports whether a limit is set.
func (l KeyLimits) Any() bool {
	if l.Quota >= 0 {
		return true
	}
	for _, w := range l.Windows {
		if w >= 0 {
			return true
		}
	}
	return false
}

const keyLua = `
local function kprune(kh, khv, now)
  local expired = redis.call('ZRANGEBYSCORE', kh, '-inf', now)
  for _, m in ipairs(expired) do redis.call('HDEL', khv, m) end
  if #expired > 0 then redis.call('ZREMRANGEBYSCORE', kh, '-inf', now) end
  local sum = 0
  for _, v in ipairs(redis.call('HVALS', khv)) do sum = sum + tonumber(v) end
  return sum
end
local function wsum(hk, mprefix, nowmin, span)
  local start = nowmin - span + 1
  local sh = math.floor(start / 60)
  local total = 0
  local hv = redis.call('HGETALL', hk)
  for i = 1, #hv, 2 do
    if tonumber(hv[i]) > sh then total = total + tonumber(hv[i + 1]) end
  end
  local mv = redis.call('HGETALL', mprefix .. sh)
  for i = 1, #mv, 2 do
    if tonumber(mv[i]) >= start then total = total + tonumber(mv[i + 1]) end
  end
  return total
end
`

var keyStatusScript = redis.NewScript(keyLua + `
-- KEYS: kb, kp, kh, khv, kwr, kwh:a, kwh:b ; ARGV: now_ms, now_min, mprefix_a, mprefix_b
local base = redis.call('HMGET', KEYS[1], 'a', 'b', 'v')
if not base[1] then return {-1} end
if redis.call('EXISTS', KEYS[5]) == 0 then return {-2} end
local now = tonumber(ARGV[1])
local nowmin = tonumber(ARGV[2])
local pend = redis.call('HMGET', KEYS[2], 'a', 'b')
local held = kprune(KEYS[3], KEYS[4], now)
local out = {1, tonumber(base[1]) + tonumber(pend[1] or '0'), tonumber(base[2]) + tonumber(pend[2] or '0'), held, tonumber(base[3])}
for _, span in ipairs({300, 1440, 10080}) do
  table.insert(out, wsum(KEYS[6], ARGV[3], nowmin, span))
  table.insert(out, wsum(KEYS[7], ARGV[4], nowmin, span))
end
return out
`)

// KeyUsage is a key meter's real-time state.
type KeyUsage struct {
	UsedAll, UsedBalance int64
	Held                 int64
	Version              int64
	// WindowAll / WindowBalance are the 5h, 1d and 7d sums.
	WindowAll, WindowBalance [3]int64
}

// Used returns the usage of a scope ("a" or "b").
func (u KeyUsage) Used(scope string) int64 {
	if scope == "b" {
		return u.UsedBalance
	}
	return u.UsedAll
}

// Window returns window i of a scope.
func (u KeyUsage) Window(scope string, i int) int64 {
	if scope == "b" {
		return u.WindowBalance[i]
	}
	return u.WindowAll[i]
}

// KeyUsage reads a key meter. ErrKeyBaseMissing / ErrKeyWindowsMissing
// ask the caller to rebuild.
func (s *State) KeyUsage(ctx context.Context, key uuid.UUID, now time.Time) (KeyUsage, error) {
	kb, kp, kh, khv, kwr := s.keyKeys(key)
	res, err := keyStatusScript.Run(ctx, s.R, []string{kb, kp, kh, khv, kwr, s.keyHourKey(key, "a"), s.keyHourKey(key, "b")},
		now.UnixMilli(), now.Unix()/60, s.keyMinutePrefix(key, "a"), s.keyMinutePrefix(key, "b")).Int64Slice()
	if err != nil {
		return KeyUsage{}, err
	}
	switch res[0] {
	case -1:
		return KeyUsage{}, ErrKeyBaseMissing
	case -2:
		return KeyUsage{}, ErrKeyWindowsMissing
	}
	u := KeyUsage{UsedAll: res[1], UsedBalance: res[2], Held: res[3], Version: res[4]}
	for i := 0; i < 3; i++ {
		u.WindowAll[i], u.WindowBalance[i] = res[5+2*i], res[6+2*i]
	}
	return u, nil
}

var keySyncScript = redis.NewScript(`
local cur = redis.call('HGET', KEYS[1], 'v')
if cur and tonumber(cur) >= tonumber(ARGV[3]) then return 0 end
redis.call('HSET', KEYS[1], 'a', ARGV[1], 'b', ARGV[2], 'v', ARGV[3])
return 1
`)

// SyncKeyBase installs a committed (all, balance, version) unless Redis
// already holds the same or a newer version.
func (s *State) SyncKeyBase(ctx context.Context, key uuid.UUID, all, balance, version int64) error {
	kb, _, _, _, _ := s.keyKeys(key)
	return keySyncScript.Run(ctx, s.R, []string{kb}, all, balance, version).Err()
}

// MinuteAmount is one minute bucket of a rebuild.
type MinuteAmount struct {
	Minute       int64
	All, Balance int64
}

// RebuildKeyWindows replaces a key's window buckets with the given minute
// amounts (from request_logs) and marks them complete.
func (s *State) RebuildKeyWindows(ctx context.Context, key uuid.UUID, rows []MinuteAmount, now time.Time) error {
	_, _, _, _, kwr := s.keyKeys(key)
	ha, hb := s.keyHourKey(key, "a"), s.keyHourKey(key, "b")
	pa, pb := s.keyMinutePrefix(key, "a"), s.keyMinutePrefix(key, "b")
	nowHour := now.Unix() / 3600
	pipe := s.R.TxPipeline()
	pipe.Del(ctx, ha, hb)
	for h := nowHour - int64(KeyBucketRetention/time.Hour); h <= nowHour; h++ {
		pipe.Del(ctx, pa+strconv.FormatInt(h, 10), pb+strconv.FormatInt(h, 10))
	}
	ttl := KeyBucketRetention
	for _, r := range rows {
		h := strconv.FormatInt(r.Minute/60, 10)
		m := strconv.FormatInt(r.Minute, 10)
		if r.All != 0 {
			pipe.HIncrBy(ctx, ha, h, r.All)
			pipe.HIncrBy(ctx, pa+h, m, r.All)
			pipe.Expire(ctx, pa+h, ttl)
		}
		if r.Balance != 0 {
			pipe.HIncrBy(ctx, hb, h, r.Balance)
			pipe.HIncrBy(ctx, pb+h, m, r.Balance)
			pipe.Expire(ctx, pb+h, ttl)
		}
	}
	pipe.Expire(ctx, ha, ttl)
	pipe.Expire(ctx, hb, ttl)
	pipe.Set(ctx, kwr, "1", ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// MinuteBuckets returns a scope's minute buckets of the last span minutes
// (status pages and recovery time).
func (s *State) MinuteBuckets(ctx context.Context, key uuid.UUID, scope string, span int64, now time.Time) ([]MinuteAmount, error) {
	nowMin := now.Unix() / 60
	start := nowMin - span + 1
	prefix := s.keyMinutePrefix(key, scope)
	pipe := s.R.Pipeline()
	var cmds []*redis.MapStringStringCmd
	for h := start / 60; h <= nowMin/60; h++ {
		cmds = append(cmds, pipe.HGetAll(ctx, prefix+strconv.FormatInt(h, 10)))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	var out []MinuteAmount
	for _, c := range cmds {
		for f, v := range c.Val() {
			m, err := strconv.ParseInt(f, 10, 64)
			if err != nil || m < start {
				continue
			}
			n, _ := strconv.ParseInt(v, 10, 64)
			out = append(out, MinuteAmount{Minute: m, All: n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Minute < out[j].Minute })
	return out, nil
}

// RecoveryTime is when a sliding window of span minutes whose settled usage
// is in buckets (ascending) drops below limit: the start of the minute in
// which the oldest buckets that must leave have left. ok is false when the
// window is not exhausted by settled usage.
func RecoveryTime(buckets []MinuteAmount, limit, span int64) (time.Time, bool) {
	var used int64
	for _, b := range buckets {
		used += b.All
	}
	if used < limit {
		return time.Time{}, false
	}
	for _, b := range buckets {
		used -= b.All
		if used < limit {
			return time.Unix((b.Minute+span)*60, 0).UTC(), true
		}
	}
	return time.Time{}, false
}

var holdKeyScript = redis.NewScript(pruneLua + keyLua + `
-- KEYS: wb, wh, wa, wp, kb, kp, kh, khv, kwh(scope)
-- ARGV: now_ms, ttl_ms, request_id, want, use_key, scope, quota, l5h, l1d, l7d, now_min, mprefix(scope)
local now = tonumber(ARGV[1])
local want = tonumber(ARGV[4])
local b = redis.call('HGET', KEYS[1], 'b')
if not b then return {-1, 0, 0, 0} end
local held = prune(KEYS[2], KEYS[3], now)
local avail = tonumber(b) - tonumber(redis.call('GET', KEYS[4]) or '0') - held
local usekey = ARGV[5] == '1'
if usekey then
  local base = redis.call('HGET', KEYS[5], ARGV[6])
  if not base then return {-2, 0, 0, 0} end
  local used = tonumber(base) + tonumber(redis.call('HGET', KEYS[6], ARGV[6]) or '0')
  local kheld = kprune(KEYS[7], KEYS[8], now)
  local quota = tonumber(ARGV[7])
  if quota >= 0 then
    local rem = quota - used - kheld
    if rem <= 0 then return {0, 0, avail, 3} end
    if want > rem then want = rem end
  end
  local spans = {300, 1440, 10080}
  for i = 1, 3 do
    local lim = tonumber(ARGV[7 + i])
    if lim >= 0 then
      local rem = lim - wsum(KEYS[9], ARGV[12], tonumber(ARGV[11]), spans[i]) - kheld
      if rem <= 0 then return {0, 0, avail, 3 + i} end
      if want > rem then want = rem end
    end
  end
end
if avail <= 0 then return {0, 0, avail, 1} end
if want > avail then want = avail end
if want < 0 then want = 0 end
redis.call('ZADD', KEYS[2], now + tonumber(ARGV[2]), ARGV[3])
redis.call('HSET', KEYS[3], ARGV[3], want)
redis.call('PEXPIRE', KEYS[2], 86400000)
redis.call('PEXPIRE', KEYS[3], 86400000)
if usekey then
  redis.call('ZADD', KEYS[7], now + tonumber(ARGV[2]), ARGV[3])
  redis.call('HSET', KEYS[8], ARGV[3], want)
  redis.call('PEXPIRE', KEYS[7], 86400000)
  redis.call('PEXPIRE', KEYS[8], 86400000)
end
return {1, want, avail, 0}
`)

// Hold rejection reasons of HoldWithKey.
const (
	HoldOK           = 0
	HoldWallet       = 1 // wallet not positive
	HoldKeyQuota     = 3
	HoldKeyWindow5h  = 4
	HoldKeyWindow1d  = 5
	HoldKeyWindow7d  = 6
	holdWalletAbsent = -1
	holdKeyAbsent    = -2
)

// KeyHold is the key side of a hold; a nil *KeyHold holds the wallet only.
type KeyHold struct {
	Key    uuid.UUID
	Limits KeyLimits
}

// KeyHoldResult is HoldResult plus the reason of a refusal.
type KeyHoldResult struct {
	HoldResult
	Reason int
}

// HoldWithKey reserves up to want from a wallet and, when kh is given, from
// the key's quota and windows in one step: the hold is capped at the smallest
// remaining amount and refused when any of them is not positive.
func (s *State) HoldWithKey(ctx context.Context, w Wallet, kh *KeyHold, requestID string, want int64, ttl time.Duration, now time.Time) (KeyHoldResult, error) {
	wk := s.walletKeys(w)
	keys := append([]string{}, wk...)
	args := []any{now.UnixMilli(), ttl.Milliseconds(), requestID, want}
	if kh != nil {
		kb, kp, khz, khv, _ := s.keyKeys(kh.Key)
		keys = append(keys, kb, kp, khz, khv, s.keyHourKey(kh.Key, kh.Limits.Scope))
		args = append(args, "1", kh.Limits.Scope, kh.Limits.Quota, kh.Limits.Windows[0], kh.Limits.Windows[1], kh.Limits.Windows[2],
			now.Unix()/60, s.keyMinutePrefix(kh.Key, kh.Limits.Scope))
	} else {
		none := s.k("none")
		keys = append(keys, none, none, none, none, none)
		args = append(args, "0", "a", -1, -1, -1, -1, 0, "")
	}
	res, err := holdKeyScript.Run(ctx, s.R, keys, args...).Int64Slice()
	if err != nil {
		return KeyHoldResult{}, err
	}
	switch res[0] {
	case holdWalletAbsent:
		return KeyHoldResult{}, ErrWalletMissing
	case holdKeyAbsent:
		return KeyHoldResult{}, ErrKeyBaseMissing
	}
	return KeyHoldResult{HoldResult: HoldResult{OK: res[0] == 1, Held: res[1], Available: res[2]}, Reason: int(res[3])}, nil
}

// ReleaseKeyHold drops a request's key hold.
func (s *State) ReleaseKeyHold(ctx context.Context, key uuid.UUID, requestID string) error {
	_, _, kh, khv, _ := s.keyKeys(key)
	pipe := s.R.Pipeline()
	pipe.ZRem(ctx, kh, requestID)
	pipe.HDel(ctx, khv, requestID)
	_, err := pipe.Exec(ctx)
	return err
}

// ExtendKeyHold pushes a live key hold's expiry out.
func (s *State) ExtendKeyHold(ctx context.Context, key uuid.UUID, requestID string, ttl time.Duration, now time.Time) error {
	_, _, kh, _, _ := s.keyKeys(key)
	return s.R.ZAddXX(ctx, kh, redis.Z{Score: float64(now.Add(ttl).UnixMilli()), Member: requestID}).Err()
}

// DropKeyMeter deletes a key's base and window marker (tests and repair).
func (s *State) DropKeyMeter(ctx context.Context, key uuid.UUID) error {
	kb, _, _, _, kwr := s.keyKeys(key)
	return s.R.Del(ctx, kb, kwr).Err()
}
