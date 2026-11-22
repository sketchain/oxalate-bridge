package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/keymeter"
	"github.com/sketchain/oxalate-bridge/backend/internal/secret"
)

// Admin API tokens: "Authorization: Bearer oba-…" on /admin/*.

// Error codes of token authentication.
const (
	CodeAdminTokenInvalid     = "admin_token_invalid"
	CodeAdminTokenExpired     = "admin_token_expired"
	CodeAdminTokenRevoked     = "admin_token_revoked"
	CodeAdminTokenIPDenied    = "admin_token_ip_denied"
	CodeAdminTokenReadOnly    = "admin_token_read_only"
	CodeAdminTokenForbidden   = "admin_token_forbidden"
	CodeAdminTokenRateLimited = "admin_token_rate_limited"
)

const ctxToken ctxKey = 101

// tokenAuth is the admin token of a request authenticated by one.
type tokenAuth struct {
	ID    uuid.UUID
	Name  string
	Scope string
}

func currentToken(ctx context.Context) (tokenAuth, bool) {
	t, ok := ctx.Value(ctxToken).(tokenAuth)
	return t, ok
}

// errTokenForbidden refuses an operation no admin token may perform.
func errTokenForbidden(what string) error {
	return apiErr(http.StatusForbidden, CodeAdminTokenForbidden, "admin API tokens cannot "+what)
}

// bearerAdminToken extracts an "oba-" bearer token.
func bearerAdminToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	tok := strings.TrimSpace(h[7:])
	return tok, strings.HasPrefix(tok, secret.AdminTokenPrefix)
}

// tokenTouch throttles last_used updates to one write per token per minute
// on each replica.
var tokenTouch sync.Map // uuid.UUID -> time.Time

// serveAdminToken authenticates an admin request made with a token. Token
// requests skip the CSRF header check (no ambient credentials).
func (s *Server) serveAdminToken(w http.ResponseWriter, r *http.Request, next http.Handler, path, raw string) {
	ctx := r.Context()
	tok, err := s.Store.Q.GetAdminTokenByHash(ctx, secret.KeyHash(raw))
	if err != nil {
		WriteError(w, http.StatusUnauthorized, CodeAdminTokenInvalid, "unknown admin token")
		return
	}
	now := time.Now().UTC()
	if tok.RevokedAt != nil {
		WriteError(w, http.StatusUnauthorized, CodeAdminTokenRevoked, "admin token revoked")
		return
	}
	if tok.ExpiresAt != nil && !now.Before(*tok.ExpiresAt) {
		WriteError(w, http.StatusUnauthorized, CodeAdminTokenExpired, "admin token expired")
		return
	}
	creator, err := s.Store.Q.GetUser(ctx, tok.CreatedBy)
	if err != nil || creator.Status != "active" || creator.Role != "admin" {
		WriteError(w, http.StatusUnauthorized, CodeAdminTokenInvalid, "the token's creator is no longer an active admin")
		return
	}
	ip := clientIP(ctx)
	if len(tok.IpAllowlist) > 0 && !keymeter.IPAllowed(tok.IpAllowlist, ip) {
		WriteError(w, http.StatusForbidden, CodeAdminTokenIPDenied, "client IP not allowed for this admin token")
		return
	}
	if s.rateHit(ctx, "admintoken", tok.ID.String(), int64(s.Settings.Get().AdminTokenPerMinute)) {
		w.Header().Set("Retry-After", "60")
		WriteError(w, http.StatusTooManyRequests, CodeAdminTokenRateLimited, "admin token rate limit exceeded")
		return
	}
	if strings.HasPrefix(path, "/admin/admin-tokens") {
		WriteError(w, http.StatusForbidden, CodeAdminTokenForbidden, "admin API tokens cannot manage admin tokens")
		return
	}
	if tok.Scope != "write" && r.Method != http.MethodGet && r.Method != http.MethodHead {
		WriteError(w, http.StatusForbidden, CodeAdminTokenReadOnly, "read-only admin token")
		return
	}
	if last, ok := tokenTouch.Load(tok.ID); !ok || now.Sub(last.(time.Time)) >= time.Minute || tok.LastUsedIp != ip {
		tokenTouch.Store(tok.ID, now)
		if err := s.Store.Q.TouchAdminToken(context.WithoutCancel(ctx), sqlc.TouchAdminTokenParams{ID: tok.ID, LastUsedAt: &now, LastUsedIp: ip}); err != nil {
			s.Log.Warn("touch admin token", "err", err)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	ctx = context.WithValue(ctx, ctxUser, SessionUser{ID: creator.ID, Role: creator.Role})
	ctx = context.WithValue(ctx, ctxToken, tokenAuth{ID: tok.ID, Name: tok.Name, Scope: tok.Scope})
	next.ServeHTTP(w, r.WithContext(ctx))
}

func adminTokenOf(t sqlc.AdminToken, username string, now time.Time) AdminToken {
	status := AdminTokenStatus("active")
	switch {
	case t.RevokedAt != nil:
		status = "revoked"
	case t.ExpiresAt != nil && !now.Before(*t.ExpiresAt):
		status = "expired"
	}
	ips := nullable.NewNullNullable[[]string]()
	if len(t.IpAllowlist) > 0 {
		ips = nullable.NewNullableWithValue(t.IpAllowlist)
	}
	return AdminToken{Id: t.ID, Name: t.Name, Prefix: t.TokenPrefix, Scope: AdminTokenScope(t.Scope), Status: status,
		CreatedBy: t.CreatedBy, CreatedByUsername: username, ExpiresAt: nullTime(t.ExpiresAt), IpAllowlist: ips,
		LastUsedAt: nullTime(t.LastUsedAt), LastUsedIp: t.LastUsedIp, RevokedAt: nullTime(t.RevokedAt), CreatedAt: t.CreatedAt}
}

// AdminListAdminTokens implements GET /admin/admin-tokens (session only).
func (s *Server) AdminListAdminTokens(ctx context.Context, _ AdminListAdminTokensRequestObject) (AdminListAdminTokensResponseObject, error) {
	rows, err := s.Store.Q.ListAdminTokens(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	items := make([]AdminToken, len(rows))
	for i, r := range rows {
		items[i] = adminTokenOf(sqlc.AdminToken{ID: r.ID, Name: r.Name, TokenHash: r.TokenHash, TokenPrefix: r.TokenPrefix,
			Scope: r.Scope, CreatedBy: r.CreatedBy, ExpiresAt: r.ExpiresAt, IpAllowlist: r.IpAllowlist, LastUsedAt: r.LastUsedAt,
			LastUsedIp: r.LastUsedIp, RevokedAt: r.RevokedAt, CreatedAt: r.CreatedAt}, deref(r.CreatedByUsername, ""), now)
	}
	return AdminListAdminTokens200JSONResponse(AdminTokenList{Items: items}), nil
}

// AdminCreateAdminToken implements POST /admin/admin-tokens (session only).
func (s *Server) AdminCreateAdminToken(ctx context.Context, req AdminCreateAdminTokenRequestObject) (AdminCreateAdminTokenResponseObject, error) {
	if _, ok := currentToken(ctx); ok {
		return nil, errTokenForbidden("manage admin tokens")
	}
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	b := req.Body
	name := strings.TrimSpace(b.Name)
	if name == "" || len([]rune(name)) > 64 {
		return nil, badRequest("name must have 1-64 characters")
	}
	if b.Scope != "read" && b.Scope != "write" {
		return nil, badRequest("scope must be read or write")
	}
	now := time.Now().UTC()
	var expires *time.Time
	if b.ExpiresAt.IsSpecified() && !b.ExpiresAt.IsNull() {
		e := b.ExpiresAt.MustGet().UTC()
		if !e.After(now) {
			return nil, badRequest("expires_at must be in the future")
		}
		expires = &e
	}
	var ips []string
	if b.IpAllowlist.IsSpecified() && !b.IpAllowlist.IsNull() {
		list := b.IpAllowlist.MustGet()
		if len(list) > 64 {
			return nil, badRequest("at most 64 IP entries")
		}
		var err error
		if ips, err = keymeter.NormalizeIPs(list); err != nil {
			return nil, badRequest(err.Error())
		}
	}
	raw := secret.NewAdminToken()
	id, _ := uuid.NewV7()
	op := currentUser(ctx)
	t, err := s.Store.Q.CreateAdminToken(ctx, sqlc.CreateAdminTokenParams{ID: id, Name: name, TokenHash: secret.KeyHash(raw),
		TokenPrefix: secret.KeyPrefix(raw), Scope: string(b.Scope), CreatedBy: op.ID, ExpiresAt: expires, IpAllowlist: ips})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	username := ""
	if u, err := s.Store.Q.GetUser(ctx, op.ID); err == nil {
		username = u.Username
	}
	out := adminTokenOf(t, username, now)
	s.audit(ctx, "admin_token.create", "admin_token", t.ID.String(), nil, out)
	return AdminCreateAdminToken201JSONResponse(AdminTokenCreated{Token: out, Secret: raw}), nil
}

// AdminRevokeAdminToken implements POST /admin/admin-tokens/{id}/revoke.
func (s *Server) AdminRevokeAdminToken(ctx context.Context, req AdminRevokeAdminTokenRequestObject) (AdminRevokeAdminTokenResponseObject, error) {
	if _, ok := currentToken(ctx); ok {
		return nil, errTokenForbidden("manage admin tokens")
	}
	before, err := s.Store.Q.GetAdminToken(ctx, req.TokenId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if before.RevokedAt != nil {
		return nil, invalidState("token already revoked")
	}
	t, err := s.Store.Q.RevokeAdminToken(ctx, req.TokenId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	username := ""
	if u, err := s.Store.Q.GetUser(ctx, t.CreatedBy); err == nil {
		username = u.Username
	}
	now := time.Now().UTC()
	out := adminTokenOf(t, username, now)
	s.audit(ctx, "admin_token.revoke", "admin_token", t.ID.String(), adminTokenOf(before, username, now), out)
	return AdminRevokeAdminToken200JSONResponse(out), nil
}
