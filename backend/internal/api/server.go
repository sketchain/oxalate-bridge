package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/oapi-codegen/nullable"

	"github.com/sketchain/oxalate-bridge/backend/internal/authcache"
	"github.com/sketchain/oxalate-bridge/backend/internal/avail"
	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpaquota"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/events"
	"github.com/sketchain/oxalate-bridge/backend/internal/router"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/secret"
	"github.com/sketchain/oxalate-bridge/backend/internal/settings"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
	"github.com/sketchain/oxalate-bridge/backend/internal/subs"
)

// Server implements StrictServerInterface.
type Server struct {
	Store         *store.Store
	St            *rstate.State
	Box           *secret.Box
	Settings      *settings.Store
	Router        *router.Router
	Cat           *router.Catalog
	Subs          *subs.Service
	Prices        *billing.Prices
	CPA           *cpa.Client
	Hub           *events.Hub
	Auth          *authcache.Cache
	Log           *slog.Logger
	CookieSecure  bool
	RefreshPrices func(context.Context) error
	SessionTTL    time.Duration
	// CPAQuota reads the cpa-quota plugin's tables (read only).
	CPAQuota *cpaquota.Reader
	// Avail serves the cached availability snapshot.
	Avail *avail.Service
	// Location is the server time zone used to cut days (admin overview).
	Location *time.Location
}

var _ StrictServerInterface = (*Server)(nil)

// SessionCookie is the name of the panel session cookie.
const SessionCookie = "bridge_session"

// ---- errors -----------------------------------------------------------------

// APIError is a typed error rendered as {"error":{"code","message"}}.
// Details, when set, are extra members of the error object (e.g. the
// blocking subscriptions of account_has_subscriptions).
type APIError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func apiErr(status int, code, msg string) *APIError {
	return &APIError{Status: status, Code: code, Message: msg}
}

func badRequest(msg string) error   { return apiErr(http.StatusBadRequest, "validation_failed", msg) }
func notFound(what string) error    { return apiErr(http.StatusNotFound, "not_found", what+" not found") }
func conflict(msg string) error     { return apiErr(http.StatusConflict, "conflict", msg) }
func invalidState(msg string) error { return apiErr(http.StatusConflict, "invalid_state", msg) }
func forbidden(msg string) error    { return apiErr(http.StatusForbidden, "forbidden", msg) }
func unauthorized(msg string) error { return apiErr(http.StatusUnauthorized, "unauthorized", msg) }
func insufficientBalance() error {
	return apiErr(http.StatusPaymentRequired, "insufficient_balance", "余额不足")
}
func upstreamInvalid(msg string) error {
	return apiErr(http.StatusUnprocessableEntity, "upstream_validation_failed", msg)
}

// WriteError renders an error response.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var body Error
	body.Error.Code, body.Error.Message = code, msg
	_ = json.NewEncoder(w).Encode(body)
}

func writeErrorDetails(w http.ResponseWriter, ae *APIError) {
	e := map[string]any{}
	for k, v := range ae.Details {
		e[k] = v
	}
	e["code"], e["message"] = ae.Code, ae.Message
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(ae.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": e})
}

// StrictOptions maps handler errors to the error format.
func (s *Server) StrictOptions() StrictHTTPServerOptions {
	return StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			var ae *APIError
			if errors.As(err, &ae) {
				if ae.Details != nil {
					writeErrorDetails(w, ae)
					return
				}
				WriteError(w, ae.Status, ae.Code, ae.Message)
				return
			}
			s.Log.Error("panel api error", "path", r.URL.Path, "err", err)
			WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		},
	}
}

// mapDomainErr converts store/subs errors.
func mapDomainErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, subs.ErrNotFound):
		return notFound("resource")
	case errors.Is(err, store.ErrInsufficientBalance):
		return insufficientBalance()
	case errors.Is(err, subs.ErrNotOnSale):
		return invalidState("plan 已下架")
	case errors.Is(err, subs.ErrBadState):
		return invalidState("订阅当前状态不允许此操作")
	case errors.Is(err, subs.ErrAccountDisabled):
		return badRequest("账号已停用")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return conflict("already exists")
	}
	return err
}

// ---- request context ----------------------------------------------------------

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxIP
	ctxSession
)

// SessionUser is the authenticated panel user.
type SessionUser struct {
	ID   uuid.UUID
	Role string
}

// WithClientIP stores the client IP in a context.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, ctxIP, ip)
}

func clientIP(ctx context.Context) string {
	ip, _ := ctx.Value(ctxIP).(string)
	return ip
}

func currentUser(ctx context.Context) SessionUser {
	u, _ := ctx.Value(ctxUser).(SessionUser)
	return u
}

func sessionToken(ctx context.Context) string {
	t, _ := ctx.Value(ctxSession).(string)
	return t
}

type sessionData struct {
	UserID uuid.UUID `json:"user_id"`
	At     time.Time `json:"at"`
}

// Middleware enforces the CSRF header, the session and the admin role.
// prefix is the mount path ("/api/v1").
func (s *Server) Middleware(prefix string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimPrefix(r.URL.Path, prefix)
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions &&
				r.Header.Get("X-Requested-With") == "" {
				WriteError(w, http.StatusForbidden, "csrf_header_missing", "X-Requested-With header required")
				return
			}
			if path == "/auth/login" {
				next.ServeHTTP(w, r)
				return
			}
			c, err := r.Cookie(SessionCookie)
			if err != nil || c.Value == "" {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "login required")
				return
			}
			ctx := r.Context()
			raw, err := s.St.R.Get(ctx, s.St.SessionKey(c.Value)).Bytes()
			if err != nil {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "session expired")
				return
			}
			var sd sessionData
			if json.Unmarshal(raw, &sd) != nil {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "session invalid")
				return
			}
			user, err := s.Store.Q.GetUser(ctx, sd.UserID)
			if err != nil || user.Status != "active" {
				_ = s.St.DeleteSession(ctx, sd.UserID, c.Value)
				WriteError(w, http.StatusUnauthorized, "unauthorized", "user unavailable")
				return
			}
			_ = s.St.TouchSession(ctx, user.ID, c.Value, s.sessionTTL())
			if strings.HasPrefix(path, "/admin/") && user.Role != "admin" {
				WriteError(w, http.StatusForbidden, "forbidden", "admin role required")
				return
			}
			ctx = context.WithValue(ctx, ctxUser, SessionUser{ID: user.ID, Role: user.Role})
			ctx = context.WithValue(ctx, ctxSession, c.Value)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// revokeSessions signs a user out of every panel session except keep ("" =
// all). It runs after the change that requires it has been committed; the
// middleware also re-reads the user on every request, so a failure here only
// delays the effect for a password change.
func (s *Server) revokeSessions(ctx context.Context, user uuid.UUID, keep string) {
	if _, err := s.St.RevokeSessions(context.WithoutCancel(ctx), user, keep); err != nil {
		s.Log.Error("revoke sessions", "user", user, "err", err)
	}
}

func (s *Server) sessionTTL() time.Duration {
	if s.SessionTTL > 0 {
		return s.SessionTTL
	}
	return 7 * 24 * time.Hour
}

// ---- pagination ---------------------------------------------------------------

func limitOf(l *int) int32 {
	if l == nil || *l <= 0 {
		return 50
	}
	if *l > 200 {
		return 200
	}
	return int32(*l)
}

func encodeCursor(at time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(c *string) (*time.Time, *uuid.UUID, error) {
	if c == nil || *c == "" {
		return nil, nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(*c)
	if err != nil {
		return nil, nil, apiErr(http.StatusBadRequest, "bad_request", "invalid cursor")
	}
	ts, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return nil, nil, apiErr(http.StatusBadRequest, "bad_request", "invalid cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, nil, apiErr(http.StatusBadRequest, "bad_request", "invalid cursor")
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, nil, apiErr(http.StatusBadRequest, "bad_request", "invalid cursor")
	}
	return &at, &id, nil
}

func nextCursor(n int, limit int32, at time.Time, id uuid.UUID) nullable.Nullable[string] {
	if n < int(limit) {
		return nullable.NewNullNullable[string]()
	}
	return nullable.NewNullableWithValue(encodeCursor(at, id))
}

// ---- helpers ------------------------------------------------------------------

func nullUUID(p *uuid.UUID) nullable.Nullable[uuid.UUID] {
	if p == nil {
		return nullable.NewNullNullable[uuid.UUID]()
	}
	return nullable.NewNullableWithValue(*p)
}

func nullTime(p *time.Time) nullable.Nullable[time.Time] {
	if p == nil || p.IsZero() {
		return nullable.NewNullNullable[time.Time]()
	}
	return nullable.NewNullableWithValue(p.UTC())
}

func nullInt32(p *int32) nullable.Nullable[int] {
	if p == nil {
		return nullable.NewNullNullable[int]()
	}
	return nullable.NewNullableWithValue(int(*p))
}

func nullFloat(p *float64) nullable.Nullable[float64] {
	if p == nil {
		return nullable.NewNullNullable[float64]()
	}
	return nullable.NewNullableWithValue(*p)
}

// optInt32 resolves a nullable request field: unspecified keeps cur.
func optInt32(n nullable.Nullable[int], cur *int32) (*int32, error) {
	if !n.IsSpecified() {
		return cur, nil
	}
	if n.IsNull() {
		return nil, nil
	}
	v := n.MustGet()
	if v < 0 {
		return nil, badRequest("value must be >= 0")
	}
	out := int32(v)
	return &out, nil
}

func optFloat(n nullable.Nullable[float64], cur *float64) *float64 {
	if !n.IsSpecified() {
		return cur
	}
	if n.IsNull() {
		return nil
	}
	v := n.MustGet()
	return &v
}

func deref[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// audit records an admin write. Secrets must not be part of before/after.
func (s *Server) audit(ctx context.Context, action, targetType, targetID string, before, after any) {
	diff, err := json.Marshal(map[string]any{"before": before, "after": after})
	if err != nil {
		diff = []byte("{}")
	}
	op := currentUser(ctx).ID
	id, _ := uuid.NewV7()
	var opPtr *uuid.UUID
	if op != uuid.Nil {
		opPtr = &op
	}
	if err := s.Store.Q.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{ID: id, OperatorID: opPtr, Action: action,
		TargetType: targetType, TargetID: targetID, Diff: diff, Ip: clientIP(ctx)}); err != nil {
		s.Log.Error("write audit log", "action", action, "err", err)
	}
}

func fmtMicro(v int64) string { return fmt.Sprintf("$%.6f", billing.MicroToUSD(v)) }
