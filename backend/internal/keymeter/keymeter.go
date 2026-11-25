// Package keymeter enforces and reports per-key limits: expiry, IP and model
// allowlists, the total quota since the last reset and the sliding 5h / 1d /
// 7d windows. Real-time figures live in Redis (rstate key meters); this
// package rebuilds them from PostgreSQL when they are missing.
package keymeter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sketchain/oxalate-bridge/backend/internal/authcache"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/relaymsg"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
)

// Error codes of key-level rejections (also the request log error_type).
const (
	CodeExpired         = "key_expired"
	CodeIPNotAllowed    = "ip_not_allowed"
	CodeModelNotAllowed = "model_not_allowed"
	CodeQuotaExhausted  = "key_quota_exhausted"
	CodeWindowExhausted = "key_window_exhausted"
	// CodeBalanceQuotaExhausted is the quota or a window of a balance-scope
	// key running out: only the standard and force tiers are refused, the
	// plan tier is still tried.
	CodeBalanceQuotaExhausted = "key_balance_quota_exhausted"
)

// Rejection is a key-level refusal, rendered as an Anthropic error.
type Rejection struct {
	Status     int
	Type       string // permission_error | rate_limit_error
	Code       string
	Message    string
	Window     string     // window rejections only
	RecoversAt *time.Time // window rejections only, when known
	// MsgID and MsgParams name the relaymsg text of Message.
	MsgID     string
	MsgParams map[string]string
}

// Text renders the message in locale.
func (r *Rejection) Text(locale string) string {
	if r.MsgID == "" {
		return r.Message
	}
	return relaymsg.Text(locale, r.MsgID, r.MsgParams)
}

// Localize sets Message to the text in locale.
func (r *Rejection) Localize(locale string) {
	if r.MsgID != "" {
		r.Message = relaymsg.Text(locale, r.MsgID, r.MsgParams)
	}
}

// TierOnly reports whether the rejection refuses only the tiers that count
// toward a balance-scope key (standard and force) instead of the whole key.
func (r *Rejection) TierOnly() bool { return r != nil && r.Code == CodeBalanceQuotaExhausted }

// QuotaRejection is the rejection of an exhausted total quota.
func QuotaRejection(scope string) *Rejection {
	if scope == "b" {
		return localized(&Rejection{Status: http.StatusTooManyRequests, Type: "rate_limit_error", Code: CodeBalanceQuotaExhausted,
			MsgID: relaymsg.KeyBalanceQuota})
	}
	return localized(&Rejection{Status: http.StatusTooManyRequests, Type: "rate_limit_error", Code: CodeQuotaExhausted, MsgID: relaymsg.KeyQuota})
}

// WindowRejection is the rejection of an exhausted window i.
func WindowRejection(scope string, i int, recovers *time.Time) *Rejection {
	name := rstate.WindowSpans[i].Name
	rej := &Rejection{Status: http.StatusTooManyRequests, Type: "rate_limit_error", Code: CodeWindowExhausted, Window: name, RecoversAt: recovers}
	rej.MsgParams = map[string]string{"window": name}
	switch {
	case scope == "b" && recovers != nil:
		rej.Code, rej.MsgID = CodeBalanceQuotaExhausted, relaymsg.KeyBalanceWinRecover
	case scope == "b":
		rej.Code, rej.MsgID = CodeBalanceQuotaExhausted, relaymsg.KeyBalanceWindow
	case recovers != nil:
		rej.MsgID = relaymsg.KeyWindowRecover
	default:
		rej.MsgID = relaymsg.KeyWindow
	}
	if recovers != nil {
		rej.MsgParams["recovers_at"] = recovers.UTC().Format(time.RFC3339)
	}
	return localized(rej)
}

// localized fills Message in the default locale (callers that answer a
// client call Localize with the configured one).
func localized(r *Rejection) *Rejection {
	r.Localize(relaymsg.ZH)
	return r
}

// Extra is the extra members of the error object.
func (r *Rejection) Extra() map[string]any {
	out := map[string]any{"code": r.Code}
	if r.Window != "" {
		out["window"] = r.Window
	}
	if r.RecoversAt != nil {
		out["recovers_at"] = r.RecoversAt.UTC().Format(time.RFC3339)
	}
	return out
}

// Meter reads and rebuilds key meters.
type Meter struct {
	St *rstate.State
	Q  *sqlc.Queries
}

// Rebuild installs the committed base from PostgreSQL and/or the window
// buckets from the last 7 days of request logs.
func (m *Meter) rebuild(ctx context.Context, key uuid.UUID, missing error, now time.Time) error {
	if errors.Is(missing, rstate.ErrKeyBaseMissing) {
		row, err := m.Q.GetAPIKeyUsageBase(ctx, key)
		if errors.Is(err, pgx.ErrNoRows) {
			return m.St.SyncKeyBase(ctx, key, 0, 0, 0)
		}
		if err != nil {
			return err
		}
		return m.St.SyncKeyBase(ctx, key, row.UsedAllMicro, row.UsedBalanceMicro, row.UsageVersion)
	}
	rows, err := m.Q.KeyBilledByMinute(ctx, sqlc.KeyBilledByMinuteParams{KeyID: &key, CreatedAt: now.Add(-7 * 24 * time.Hour)})
	if err != nil {
		return err
	}
	out := make([]rstate.MinuteAmount, len(rows))
	for i, r := range rows {
		out[i] = rstate.MinuteAmount{Minute: r.Minute, All: r.AllMicro, Balance: r.BalanceMicro}
	}
	return m.St.RebuildKeyWindows(ctx, key, out, now)
}

// Usage returns a key meter, rebuilding missing parts first.
func (m *Meter) Usage(ctx context.Context, key uuid.UUID, now time.Time) (rstate.KeyUsage, error) {
	for attempt := 0; attempt < 3; attempt++ {
		u, err := m.St.KeyUsage(ctx, key, now)
		if errors.Is(err, rstate.ErrKeyBaseMissing) || errors.Is(err, rstate.ErrKeyWindowsMissing) {
			if err := m.rebuild(ctx, key, err, now); err != nil {
				return rstate.KeyUsage{}, err
			}
			continue
		}
		return u, err
	}
	return rstate.KeyUsage{}, rstate.ErrKeyBaseMissing
}

// EnsureBase installs a missing committed base (hold path).
func (m *Meter) EnsureBase(ctx context.Context, key uuid.UUID, now time.Time) error {
	return m.rebuild(ctx, key, rstate.ErrKeyBaseMissing, now)
}

// Recovery is when window i (0 = 5h, 1 = 1d, 2 = 7d) of a scope is expected
// to allow requests again; nil when unknown.
func (m *Meter) Recovery(ctx context.Context, key uuid.UUID, scope string, i int, limit int64, now time.Time) *time.Time {
	span := rstate.WindowSpans[i].Minutes
	buckets, err := m.St.MinuteBuckets(ctx, key, scope, span, now)
	if err != nil {
		return nil
	}
	if t, ok := rstate.RecoveryTime(buckets, limit, span); ok {
		return &t
	}
	return nil
}

// CheckStatic applies expiry, the IP allowlist and the model allowlist.
func CheckStatic(info authcache.Info, ip, normModel string, now time.Time) *Rejection {
	if info.Expired(now) {
		return localized(&Rejection{Status: http.StatusForbidden, Type: "permission_error", Code: CodeExpired, MsgID: relaymsg.KeyExpired})
	}
	if len(info.IPAllowlist) > 0 && !IPAllowed(info.IPAllowlist, ip) {
		return localized(&Rejection{Status: http.StatusForbidden, Type: "permission_error", Code: CodeIPNotAllowed,
			MsgID: relaymsg.IPNotAllowed})
	}
	if !ModelAllowed(info.ModelAllowlist, normModel) {
		return localized(&Rejection{Status: http.StatusForbidden, Type: "permission_error", Code: CodeModelNotAllowed,
			MsgID: relaymsg.ModelNotAllowed, MsgParams: map[string]string{"model": normModel}})
	}
	return nil
}

// ModelAllowed reports whether a normalized model passes an allowlist
// (empty = any).
func ModelAllowed(allow []string, normModel string) bool {
	return len(allow) == 0 || slices.Contains(allow, normModel)
}

// IPAllowed reports whether ip is inside one of the prefixes.
func IPAllowed(allow []string, ip string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range allow {
		if pfx, err := netip.ParsePrefix(p); err == nil && pfx.Contains(addr) {
			return true
		}
	}
	return false
}

// NormalizeIPs validates IPs / CIDR prefixes and returns their canonical
// prefixes (a bare address becomes /32 or /128). Empty input is nil.
func NormalizeIPs(in []string) ([]string, error) {
	var out []string
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		var pfx netip.Prefix
		if strings.Contains(v, "/") {
			p, err := netip.ParsePrefix(v)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q", v)
			}
			pfx = p.Masked()
		} else {
			a, err := netip.ParseAddr(v)
			if err != nil {
				return nil, fmt.Errorf("invalid IP %q", v)
			}
			a = a.Unmap()
			pfx = netip.PrefixFrom(a, a.BitLen())
		}
		if !slices.Contains(out, pfx.String()) {
			out = append(out, pfx.String())
		}
	}
	return out, nil
}

// CheckLimits applies the total quota and the windows of the key's scope.
// Held amounts count, so concurrent requests cannot all pass the last cent.
// For a balance-scope key the rejection is TierOnly: the caller keeps
// trying the plan tier.
func (m *Meter) CheckLimits(ctx context.Context, info authcache.Info, now time.Time) (*Rejection, error) {
	lim := info.Limits()
	if !lim.Any() {
		return nil, nil
	}
	u, err := m.Usage(ctx, info.KeyID, now)
	if err != nil {
		return nil, err
	}
	if lim.Quota >= 0 && lim.Quota-u.Used(lim.Scope)-u.Held <= 0 {
		return QuotaRejection(lim.Scope), nil
	}
	for i, l := range lim.Windows {
		if l < 0 || l-u.Window(lim.Scope, i)-u.Held > 0 {
			continue
		}
		return WindowRejection(lim.Scope, i, m.Recovery(ctx, info.KeyID, lim.Scope, i, l, now)), nil
	}
	return nil, nil
}

// HoldRejection maps a refused key hold to a rejection.
func (m *Meter) HoldRejection(ctx context.Context, info authcache.Info, reason int, now time.Time) *Rejection {
	lim := info.Limits()
	switch reason {
	case rstate.HoldKeyQuota:
		return QuotaRejection(lim.Scope)
	case rstate.HoldKeyWindow5h, rstate.HoldKeyWindow1d, rstate.HoldKeyWindow7d:
		i := reason - rstate.HoldKeyWindow5h
		return WindowRejection(lim.Scope, i, m.Recovery(ctx, info.KeyID, lim.Scope, i, lim.Windows[i], now))
	}
	return nil
}
