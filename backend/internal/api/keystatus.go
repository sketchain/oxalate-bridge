package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/sketchain/oxalate-bridge/backend/internal/authcache"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
)

// Public key query limits (per client IP and per key, each per minute).
const (
	publicPerIP  = 30
	publicPerKey = 30
)

type publicCtx struct {
	key string
	ip  string
}

const ctxPublic ctxKey = 100

// withPublic stores the API key header and client IP of a public request.
func withPublic(ctx context.Context, key, ip string) context.Context {
	return context.WithValue(ctx, ctxPublic, publicCtx{key: key, ip: ip})
}

// rateHit counts one hit in a fixed one-minute window and reports whether
// the limit is exceeded. A Redis failure lets the request through.
func (s *Server) rateHit(ctx context.Context, kind, id string, limit int64) bool {
	k := s.St.Key("rl", kind, id, strconv.FormatInt(time.Now().Unix()/60, 10))
	pipe := s.St.R.TxPipeline()
	n := pipe.Incr(ctx, k)
	pipe.Expire(ctx, k, 2*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		return false
	}
	return n.Val() > limit
}

// KeyStatusOf renders the compact status of a key (public query and
// GET /v1/bridge/status). Owner figures only when the key allows them.
func (s *Server) KeyStatusOf(ctx context.Context, info authcache.Info) (KeyStatus, error) {
	k, err := s.Store.Q.GetAPIKey(ctx, info.KeyID)
	if err != nil {
		return KeyStatus{}, mapDomainErr(err)
	}
	now := time.Now().UTC()
	limits, err := s.KeyLimits(ctx, k, now)
	if err != nil {
		return KeyStatus{}, err
	}
	out := KeyStatus{GeneratedAt: now, Limits: limits, Key: KeyStatusKey{Name: k.Name, Prefix: k.KeyPrefix,
		Status: keyStatus(k.Enabled, info.UserActive, k.ExpiresAt, now), ExpiresAt: nullTime(k.ExpiresAt),
		QuotaScope: limits.QuotaScope, ModelAllowlist: nullStrings(k.ModelAllowlist)}}
	if !k.AllowOwnerQuery {
		return out, nil
	}
	av, err := s.availabilityFor(ctx, k.UserID)
	if err != nil {
		return KeyStatus{}, err
	}
	owner := &OwnerSummary{BalanceAvailableMicro: av.BalanceAvailableMicro, Normal: av.Normal, Force: av.Force, Plans: []OwnerPlanSummary{}}
	for _, p := range av.Plans {
		ps := OwnerPlanSummary{SubscriptionId: p.SubscriptionId, PlanName: p.PlanName, Status: OwnerPlanSummaryStatus(p.Status),
			FiveHourAvailableMicro: nullable.NewNullNullable[int64](), PeriodAvailableMicro: nullable.NewNullNullable[int64](), PeriodEnd: p.PeriodEnd}
		if f, err := p.FiveHour.Get(); err == nil {
			ps.FiveHourAvailableMicro = nullable.NewNullableWithValue(f.AvailableMicro)
		}
		if f, err := p.Period.Get(); err == nil {
			ps.PeriodAvailableMicro = nullable.NewNullableWithValue(f.AvailableMicro)
		}
		if !ps.PeriodEnd.IsSpecified() {
			ps.PeriodEnd = nullable.NewNullNullable[time.Time]()
		}
		owner.Plans = append(owner.Plans, ps)
	}
	out.Account = owner
	return out, nil
}

// GetPublicKeyUsage implements GET /public/key-usage.
func (s *Server) GetPublicKeyUsage(ctx context.Context, _ GetPublicKeyUsageRequestObject) (GetPublicKeyUsageResponseObject, error) {
	pc, _ := ctx.Value(ctxPublic).(publicCtx)
	if s.rateHit(ctx, "pq-ip", pc.ip, publicPerIP) {
		return nil, apiErr(http.StatusTooManyRequests, "rate_limited", "too many queries, try again in a minute")
	}
	if pc.key == "" {
		return nil, unauthorized("API key required in Authorization: Bearer or x-api-key")
	}
	hash := authcache.HashHex(pc.key)
	if s.rateHit(ctx, "pq-key", hash, publicPerKey) {
		return nil, apiErr(http.StatusTooManyRequests, "rate_limited", "too many queries, try again in a minute")
	}
	info, err := s.Auth.Lookup(ctx, pc.key)
	if err != nil {
		return nil, err
	}
	if !info.Found {
		return nil, unauthorized("invalid API key")
	}
	st, err := s.KeyStatusOf(ctx, info)
	if err != nil {
		if ae, ok := err.(*APIError); ok && ae.Status == http.StatusNotFound {
			return nil, unauthorized("invalid API key")
		}
		return nil, err
	}
	logs, err := s.Store.Q.ListRecentKeyLogs(ctx, sqlcRecentLogs(info.KeyID))
	if err != nil {
		return nil, err
	}
	recent := make([]MyRequestLog, len(logs))
	for i, r := range logs {
		recent[i] = myLogOf(r)
	}
	return GetPublicKeyUsage200JSONResponse{GeneratedAt: st.GeneratedAt, Key: st.Key, Limits: st.Limits, Account: st.Account, Recent: recent}, nil
}

func sqlcRecentLogs(key uuid.UUID) sqlc.ListRecentKeyLogsParams {
	// Partition pruning: the latest 50 requests within the last 90 days.
	return sqlc.ListRecentKeyLogsParams{KeyID: &key, CreatedAt: time.Now().UTC().Add(-90 * 24 * time.Hour), Limit: 50}
}
