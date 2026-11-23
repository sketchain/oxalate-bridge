package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/sketchain/oxalate-bridge/backend/internal/authcache"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
)

func sqlcGetPeriod(sub uuid.UUID, start time.Time) sqlc.GetPeriodParams {
	return sqlc.GetPeriodParams{SubscriptionID: sub, PeriodStart: start}
}

func sqlcSetAccountError(id uuid.UUID, reason string) sqlc.SetAccountErrorParams {
	return sqlc.SetAccountErrorParams{ID: id, ErrorReason: reason}
}

// ServeStatus implements GET /v1/bridge/status: the key's compact status.
// It authenticates exactly like the relay endpoints but is neither billed
// nor logged.
func (p *Proxy) ServeStatus(w http.ResponseWriter, r *http.Request) {
	auth, ok := p.authenticateAny(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	// Its own per-key budget (runtime setting), not the key's RPM: status
	// lines refresh often and are never billed.
	limit := p.Settings.Get().StatusKeyPerMinute
	if !p.allowStatus(ctx, auth.KeyID, limit) {
		writeError(w, http.StatusTooManyRequests, "rate_limit_error", "status requests are limited to "+strconv.Itoa(limit)+" per minute per key")
		return
	}
	body, err := p.Status(ctx, auth)
	if err != nil {
		p.Log.Error("bridge status", "err", err)
		writeError(w, http.StatusServiceUnavailable, "api_error", "status temporarily unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

func (p *Proxy) allowStatus(ctx context.Context, key uuid.UUID, limit int) bool {
	k := p.St.Key("rl", "status", key.String(), strconv.FormatInt(p.now().Unix()/60, 10))
	pipe := p.St.R.TxPipeline()
	n := pipe.Incr(ctx, k)
	pipe.Expire(ctx, k, 2*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		return true
	}
	return n.Val() <= int64(limit)
}

// authenticateAny is authenticate that also accepts disabled and expired
// keys (the status endpoint reports them); unknown keys are 401.
func (p *Proxy) authenticateAny(w http.ResponseWriter, r *http.Request) (authcache.Info, bool) {
	key := apiKey(r)
	if key == "" {
		writeError(w, http.StatusUnauthorized, "authentication_error", "missing API key")
		return authcache.Info{}, false
	}
	info, err := p.Auth.Lookup(r.Context(), key)
	if err != nil {
		p.Log.Error("auth lookup", "err", err)
		writeError(w, http.StatusServiceUnavailable, "api_error", "authentication temporarily unavailable")
		return authcache.Info{}, false
	}
	if !info.Found {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid API key")
		return authcache.Info{}, false
	}
	return info, true
}

// APIKeyFromRequest extracts the client's API key (Authorization: Bearer or
// x-api-key; never the URL).
func APIKeyFromRequest(r *http.Request) string { return apiKey(r) }
