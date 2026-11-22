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
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
)

// Error codes of key-level rejections (also the request log error_type).
const (
	CodeExpired         = "key_expired"
	CodeIPNotAllowed    = "ip_not_allowed"
	CodeModelNotAllowed = "model_not_allowed"
	CodeQuotaExhausted  = "key_quota_exhausted"
	CodeWindowExhausted = "key_window_exhausted"
)

// Rejection is a key-level refusal, rendered as an Anthropic error.
type Rejection struct {
	Status     int
	Type       string // permission_error | rate_limit_error
	Code       string
	Message    string
	Window     string     // key_window_exhausted only
	RecoversAt *time.Time // key_window_exhausted only, when known
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
		return &Rejection{Status: http.StatusForbidden, Type: "permission_error", Code: CodeExpired, Message: "API key has expired"}
	}
	if len(info.IPAllowlist) > 0 && !IPAllowed(info.IPAllowlist, ip) {
		return &Rejection{Status: http.StatusForbidden, Type: "permission_error", Code: CodeIPNotAllowed,
			Message: "client IP is not in the key's IP allowlist"}
	}
	if !ModelAllowed(info.ModelAllowlist, normModel) {
		return &Rejection{Status: http.StatusForbidden, Type: "permission_error", Code: CodeModelNotAllowed,
			Message: fmt.Sprintf("model %q is not allowed for this API key", normModel)}
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
		return &Rejection{Status: http.StatusTooManyRequests, Type: "rate_limit_error", Code: CodeQuotaExhausted,
			Message: "API key quota exhausted"}, nil
	}
	for i, l := range lim.Windows {
		if l < 0 || l-u.Window(lim.Scope, i)-u.Held > 0 {
			continue
		}
		name := rstate.WindowSpans[i].Name
		rej := &Rejection{Status: http.StatusTooManyRequests, Type: "rate_limit_error", Code: CodeWindowExhausted, Window: name,
			RecoversAt: m.Recovery(ctx, info.KeyID, lim.Scope, i, l, now)}
		rej.Message = "API key " + name + " spending limit reached"
		if rej.RecoversAt != nil {
			rej.Message += "; expected to recover at " + rej.RecoversAt.UTC().Format(time.RFC3339)
		}
		return rej, nil
	}
	return nil, nil
}

// HoldRejection maps a refused key hold to a rejection.
func (m *Meter) HoldRejection(ctx context.Context, info authcache.Info, reason int, now time.Time) *Rejection {
	switch reason {
	case rstate.HoldKeyQuota:
		return &Rejection{Status: http.StatusTooManyRequests, Type: "rate_limit_error", Code: CodeQuotaExhausted, Message: "API key quota exhausted"}
	case rstate.HoldKeyWindow5h, rstate.HoldKeyWindow1d, rstate.HoldKeyWindow7d:
		i := reason - rstate.HoldKeyWindow5h
		lim := info.Limits()
		name := rstate.WindowSpans[i].Name
		return &Rejection{Status: http.StatusTooManyRequests, Type: "rate_limit_error", Code: CodeWindowExhausted, Window: name,
			Message: "API key " + name + " spending limit reached", RecoversAt: m.Recovery(ctx, info.KeyID, lim.Scope, i, lim.Windows[i], now)}
	}
	return nil
}
