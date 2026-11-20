package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/router"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/secret"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
)

// ---- auth ---------------------------------------------------------------------

const (
	loginWindow    = 15 * time.Minute
	loginPerIP     = 30
	loginPerUser   = 10
	dummyPHCSecret = "$argon2id$v=19$m=65536,t=2,p=2$c2FsdHNhbHRzYWx0c2FsdA$Z2FyYmFnZWdhcmJhZ2VnYXJiYWdlZ2FyYmFnZTEyMzQ"
)

func (s *Server) loginLimited(ctx context.Context, username string) bool {
	ipKey := s.St.LoginLimitKey("ip:" + clientIP(ctx))
	userKey := s.St.LoginLimitKey("u:" + strings.ToLower(username))
	pipe := s.St.R.TxPipeline()
	ipN := pipe.Incr(ctx, ipKey)
	pipe.ExpireNX(ctx, ipKey, loginWindow)
	userN := pipe.Incr(ctx, userKey)
	pipe.ExpireNX(ctx, userKey, loginWindow)
	if _, err := pipe.Exec(ctx); err != nil {
		return false
	}
	return ipN.Val() > loginPerIP || userN.Val() > loginPerUser
}

func (s *Server) cookie(value string, maxAge int) string {
	c := &http.Cookie{Name: SessionCookie, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.CookieSecure, MaxAge: maxAge}
	return c.String()
}

func meOf(u sqlc.User) Me {
	return Me{Id: u.ID, Username: u.Username, Role: Role(u.Role), Status: UserStatus(u.Status), BalanceMicro: u.BalanceMicro,
		MaxConcurrency: int(u.MaxConcurrency), Rpm: nullInt32(u.Rpm), CreatedAt: u.CreatedAt}
}

// Login implements POST /auth/login.
func (s *Server) Login(ctx context.Context, req LoginRequestObject) (LoginResponseObject, error) {
	if req.Body == nil || req.Body.Username == "" || req.Body.Password == "" {
		return nil, badRequest("username and password are required")
	}
	if s.loginLimited(ctx, req.Body.Username) {
		return nil, apiErr(http.StatusTooManyRequests, "rate_limited", "too many login attempts, try again later")
	}
	user, err := s.Store.Q.GetUserByUsername(ctx, req.Body.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		secret.CheckPassword(dummyPHCSecret, req.Body.Password) // equalize timing
		return nil, unauthorized("invalid username or password")
	}
	if err != nil {
		return nil, err
	}
	if !secret.CheckPassword(user.PasswordHash, req.Body.Password) {
		return nil, unauthorized("invalid username or password")
	}
	if user.Status != "active" {
		return nil, forbidden("user is disabled")
	}
	_ = s.St.R.Del(ctx, s.St.LoginLimitKey("u:"+strings.ToLower(req.Body.Username))).Err()
	token := secret.RandomToken(32)
	raw, _ := json.Marshal(sessionData{UserID: user.ID, At: time.Now().UTC()})
	if err := s.St.R.Set(ctx, s.St.SessionKey(token), raw, s.sessionTTL()).Err(); err != nil {
		return nil, err
	}
	cookie := s.cookie(token, int(s.sessionTTL().Seconds()))
	return Login200JSONResponse{Body: meOf(user), Headers: Login200ResponseHeaders{SetCookie: &cookie}}, nil
}

// Logout implements POST /auth/logout.
func (s *Server) Logout(ctx context.Context, _ LogoutRequestObject) (LogoutResponseObject, error) {
	if t := sessionToken(ctx); t != "" {
		_ = s.St.R.Del(ctx, s.St.SessionKey(t)).Err()
	}
	cookie := s.cookie("", -1)
	return Logout204Response{Headers: Logout204ResponseHeaders{SetCookie: &cookie}}, nil
}

// GetMe implements GET /me.
func (s *Server) GetMe(ctx context.Context, _ GetMeRequestObject) (GetMeResponseObject, error) {
	u, err := s.Store.Q.GetUser(ctx, currentUser(ctx).ID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return GetMe200JSONResponse(meOf(u)), nil
}

// ChangePassword implements POST /me/password.
func (s *Server) ChangePassword(ctx context.Context, req ChangePasswordRequestObject) (ChangePasswordResponseObject, error) {
	if req.Body == nil || len(req.Body.NewPassword) < 8 {
		return nil, badRequest("new password must have at least 8 characters")
	}
	u, err := s.Store.Q.GetUser(ctx, currentUser(ctx).ID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if !secret.CheckPassword(u.PasswordHash, req.Body.OldPassword) {
		return nil, apiErr(http.StatusBadRequest, "validation_failed", "old password is wrong")
	}
	if err := s.Store.Q.UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{ID: u.ID, PasswordHash: secret.HashPassword(req.Body.NewPassword)}); err != nil {
		return nil, err
	}
	return ChangePassword204Response{}, nil
}

// ---- keys -----------------------------------------------------------------------

func keyOf(k sqlc.ApiKey) ApiKey {
	tiers := make([]Tier, len(k.Tiers))
	for i, t := range k.Tiers {
		tiers[i] = Tier(t)
	}
	return ApiKey{Id: k.ID, Name: k.Name, Prefix: k.KeyPrefix, Tiers: tiers, SubscriptionId: nullUUID(k.SubscriptionID),
		MaxConcurrency: nullInt32(k.MaxConcurrency), Rpm: nullInt32(k.Rpm), Enabled: k.Enabled, Note: k.Note,
		LastUsedAt: nullTime(k.LastUsedAt), CreatedAt: k.CreatedAt}
}

// normalizeTiers validates an ordered tier subset.
func normalizeTiers(in []Tier) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, t := range in {
		switch string(t) {
		case router.TierPlan, router.TierStandard, router.TierForce:
		default:
			return nil, badRequest(fmt.Sprintf("unknown tier %q", t))
		}
		if slices.Contains(out, string(t)) {
			return nil, badRequest(fmt.Sprintf("duplicate tier %q", t))
		}
		out = append(out, string(t))
	}
	if len(out) == 0 {
		return nil, badRequest("at least one tier is required")
	}
	return out, nil
}

func (s *Server) checkKeySubscription(ctx context.Context, user uuid.UUID, tiers []string, sub *uuid.UUID) error {
	if !slices.Contains(tiers, router.TierPlan) {
		return nil
	}
	if sub == nil {
		return badRequest("subscription_id is required when tiers contain plan")
	}
	row, err := s.Store.Q.GetSubscription(ctx, *sub)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.UserID != user) {
		return badRequest("subscription not found")
	}
	if err != nil {
		return err
	}
	if row.Status == "cancelled" {
		return badRequest("subscription is cancelled")
	}
	return nil
}

func (s *Server) ownKey(ctx context.Context, id uuid.UUID) (sqlc.ApiKey, error) {
	k, err := s.Store.Q.GetAPIKey(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && k.UserID != currentUser(ctx).ID) {
		return k, notFound("key")
	}
	return k, err
}

// ListKeys implements GET /keys.
func (s *Server) ListKeys(ctx context.Context, _ ListKeysRequestObject) (ListKeysResponseObject, error) {
	rows, err := s.Store.Q.ListAPIKeysByUser(ctx, currentUser(ctx).ID)
	if err != nil {
		return nil, err
	}
	items := make([]ApiKey, len(rows))
	for i, k := range rows {
		items[i] = keyOf(k)
	}
	return ListKeys200JSONResponse{Items: items}, nil
}

// CreateKey implements POST /keys.
func (s *Server) CreateKey(ctx context.Context, req CreateKeyRequestObject) (CreateKeyResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	b := req.Body
	user := currentUser(ctx).ID
	var sub *uuid.UUID
	if b.SubscriptionId.IsSpecified() && !b.SubscriptionId.IsNull() {
		v := b.SubscriptionId.MustGet()
		sub = &v
	}
	var tiers []string
	var err error
	if b.Tiers == nil {
		tiers = []string{router.TierStandard}
		if sub != nil {
			tiers = []string{router.TierPlan, router.TierStandard}
		}
	} else if tiers, err = normalizeTiers(*b.Tiers); err != nil {
		return nil, err
	}
	if err := s.checkKeySubscription(ctx, user, tiers, sub); err != nil {
		return nil, err
	}
	maxConc, err := optInt32(b.MaxConcurrency, nil)
	if err != nil {
		return nil, err
	}
	rpm, err := optInt32(b.Rpm, nil)
	if err != nil {
		return nil, err
	}
	raw := secret.NewAPIKey()
	id, _ := uuid.NewV7()
	var row sqlc.ApiKey
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		row, err = tx.Q.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{ID: id, UserID: user, Name: deref(b.Name, ""),
			KeyHash: secret.KeyHash(raw), KeyEnc: s.Box.Seal(raw), KeyPrefix: secret.KeyPrefix(raw), Tiers: tiers,
			SubscriptionID: sub, MaxConcurrency: maxConc, Rpm: rpm, Enabled: deref(b.Enabled, true), Note: deref(b.Note, "")})
		if err != nil {
			return err
		}
		return tx.InvalidateHashes(ctx, [][]byte{row.KeyHash})
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	k := keyOf(row)
	return CreateKey201JSONResponse{Id: k.Id, Name: k.Name, Prefix: k.Prefix, Tiers: k.Tiers, SubscriptionId: k.SubscriptionId,
		MaxConcurrency: k.MaxConcurrency, Rpm: k.Rpm, Enabled: k.Enabled, Note: k.Note, LastUsedAt: k.LastUsedAt,
		CreatedAt: k.CreatedAt, Key: raw}, nil
}

// GetKey implements GET /keys/{keyId}.
func (s *Server) GetKey(ctx context.Context, req GetKeyRequestObject) (GetKeyResponseObject, error) {
	k, err := s.ownKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	return GetKey200JSONResponse(keyOf(k)), nil
}

// GetKeySecret implements GET /keys/{keyId}/secret.
func (s *Server) GetKeySecret(ctx context.Context, req GetKeySecretRequestObject) (GetKeySecretResponseObject, error) {
	k, err := s.ownKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	raw, err := s.Box.Open(k.KeyEnc)
	if err != nil {
		return nil, err
	}
	return GetKeySecret200JSONResponse{Key: raw}, nil
}

// UpdateKey implements PATCH /keys/{keyId}.
func (s *Server) UpdateKey(ctx context.Context, req UpdateKeyRequestObject) (UpdateKeyResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	k, err := s.ownKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	b := req.Body
	tiers := k.Tiers
	if b.Tiers != nil {
		if tiers, err = normalizeTiers(*b.Tiers); err != nil {
			return nil, err
		}
	}
	sub := k.SubscriptionID
	if b.SubscriptionId.IsSpecified() {
		if b.SubscriptionId.IsNull() {
			sub = nil
		} else {
			v := b.SubscriptionId.MustGet()
			sub = &v
		}
	}
	if err := s.checkKeySubscription(ctx, k.UserID, tiers, sub); err != nil {
		return nil, err
	}
	maxConc, err := optInt32(b.MaxConcurrency, k.MaxConcurrency)
	if err != nil {
		return nil, err
	}
	rpm, err := optInt32(b.Rpm, k.Rpm)
	if err != nil {
		return nil, err
	}
	var row sqlc.ApiKey
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		row, err = tx.Q.UpdateAPIKey(ctx, sqlc.UpdateAPIKeyParams{ID: k.ID, Name: deref(b.Name, k.Name), Tiers: tiers,
			SubscriptionID: sub, MaxConcurrency: maxConc, Rpm: rpm, Enabled: deref(b.Enabled, k.Enabled), Note: deref(b.Note, k.Note)})
		if err != nil {
			return err
		}
		return tx.InvalidateHashes(ctx, [][]byte{k.KeyHash})
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return UpdateKey200JSONResponse(keyOf(row)), nil
}

// DeleteKey implements DELETE /keys/{keyId}.
func (s *Server) DeleteKey(ctx context.Context, req DeleteKeyRequestObject) (DeleteKeyResponseObject, error) {
	k, err := s.ownKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.Q.SoftDeleteAPIKey(ctx, k.ID); err != nil {
			return err
		}
		return tx.InvalidateHashes(ctx, [][]byte{k.KeyHash})
	})
	if err != nil {
		return nil, err
	}
	return DeleteKey204Response{}, nil
}

// ---- balance and ledger -----------------------------------------------------------

func ledgerOf(e sqlc.LedgerEntry) LedgerEntry {
	return LedgerEntry{Id: e.ID, UserId: e.UserID, Type: LedgerType(e.Type), AmountMicro: e.AmountMicro,
		BalanceAfterMicro: e.BalanceAfterMicro, RefType: e.RefType, RefId: e.RefID, Note: e.Note,
		OperatorId: nullUUID(e.OperatorID), CreatedAt: e.CreatedAt}
}

// walletFigures returns settled (base − pending), available and pending of
// a wallet, syncing it from PostgreSQL when missing.
func (s *Server) walletFigures(ctx context.Context, w rstate.Wallet, base, version int64) (settled, available, pending int64, err error) {
	now := time.Now().UTC()
	available, err = s.St.Available(ctx, w, now)
	if errors.Is(err, rstate.ErrWalletMissing) {
		if err = s.St.SyncBase(ctx, w, base, version); err != nil {
			return
		}
		available, err = s.St.Available(ctx, w, now)
	}
	if err != nil {
		return
	}
	pending, err = s.St.Pending(ctx, w)
	if err != nil {
		return
	}
	b, _, _, err := s.St.Base(ctx, w)
	settled = b - pending
	return
}

// GetBalance implements GET /balance.
func (s *Server) GetBalance(ctx context.Context, _ GetBalanceRequestObject) (GetBalanceResponseObject, error) {
	u, err := s.Store.Q.GetUser(ctx, currentUser(ctx).ID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	settled, avail, pending, err := s.walletFigures(ctx, rstate.UserWallet(u.ID), u.BalanceMicro, u.BalanceVersion)
	if err != nil {
		return nil, err
	}
	return GetBalance200JSONResponse{BalanceMicro: u.BalanceMicro, AvailableMicro: avail, PendingMicro: pending, HeldMicro: settled - avail}, nil
}

func (s *Server) listLedger(ctx context.Context, user *uuid.UUID, typ *LedgerType, cursor *string, limit *int) (LedgerPage, error) {
	at, id, err := decodeCursor(cursor)
	if err != nil {
		return LedgerPage{}, err
	}
	lim := limitOf(limit)
	var t *string
	if typ != nil {
		v := string(*typ)
		t = &v
	}
	rows, err := s.Store.Q.ListLedger(ctx, sqlc.ListLedgerParams{Limit: lim, UserID: user, Type: t, CursorAt: at, CursorID: id})
	if err != nil {
		return LedgerPage{}, err
	}
	items := make([]LedgerEntry, len(rows))
	for i, e := range rows {
		items[i] = ledgerOf(e)
	}
	page := LedgerPage{Items: items, NextCursor: nullable.NewNullNullable[string]()}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = nextCursor(len(rows), lim, last.CreatedAt, last.ID)
	}
	return page, nil
}

// ListMyLedger implements GET /ledger.
func (s *Server) ListMyLedger(ctx context.Context, req ListMyLedgerRequestObject) (ListMyLedgerResponseObject, error) {
	user := currentUser(ctx).ID
	page, err := s.listLedger(ctx, &user, req.Params.Type, req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	return ListMyLedger200JSONResponse(page), nil
}

// ---- plans and subscriptions ------------------------------------------------------------

func planOf(p sqlc.Plan) Plan {
	return Plan{Id: p.ID, Name: p.Name, Description: p.Description, Subscription: SubscriptionKind(p.Subscription),
		WeeklyQuotaMicro: p.WeeklyQuotaMicro, PriceMicro: p.PriceMicro, DurationValue: int(p.DurationValue),
		DurationUnit: DurationUnit(p.DurationUnit), AllowedModels: p.AllowedModels, OnSale: p.OnSale,
		SortOrder: int(p.SortOrder), CreatedAt: p.CreatedAt}
}

func (s *Server) subscriptionOf(ctx context.Context, sub sqlc.PlanSubscription) Subscription {
	out := Subscription{Id: sub.ID, UserId: sub.UserID, PlanId: sub.PlanID, PlanName: sub.PlanName,
		Subscription: SubscriptionKind(sub.Subscription), WeeklyQuotaMicro: sub.WeeklyQuotaMicro, AllowedModels: sub.AllowedModels,
		DurationValue: int(sub.DurationValue), DurationUnit: DurationUnit(sub.DurationUnit), PriceMicro: sub.PriceMicro,
		AccountId: nullUUID(sub.AccountID), Status: SubscriptionStatus(sub.Status), ActivatedAt: nullTime(sub.ActivatedAt),
		ExpiresAt: nullTime(sub.ExpiresAt), AutoRenew: sub.AutoRenew, CancelledAt: nullTime(sub.CancelledAt),
		CreatedAt: sub.CreatedAt, CurrentPeriod: nullable.NewNullNullable[PeriodUsage]()}
	if sub.Status != "active" {
		return out
	}
	view, ok := s.Cat.Subscription(sub.ID)
	if !ok {
		view = &router.Subscription{ID: sub.ID, AccountID: sub.AccountID, WeeklyQuota: sub.WeeklyQuotaMicro, ActivatedAt: sub.ActivatedAt}
	}
	p := s.Cat.PeriodFor(ctx, view, time.Now().UTC())
	usage := PeriodUsage{PeriodStart: p.Start, PeriodEnd: p.End, QuotaMicro: sub.WeeklyQuotaMicro}
	base := sub.WeeklyQuotaMicro
	if row, err := s.Store.Q.GetPeriod(ctx, sqlc.GetPeriodParams{SubscriptionID: sub.ID, PeriodStart: p.Start}); err == nil {
		usage.QuotaMicro, usage.UsedBilledMicro, usage.UsedUpstreamMicro = row.QuotaMicro, row.UsedBilledMicro, row.UsedUpstreamMicro
		base = row.QuotaMicro - row.UsedBilledMicro
	}
	usage.RemainingMicro = base
	if avail, err := s.St.Available(ctx, rstate.PeriodWallet(sub.ID, p.Start), time.Now().UTC()); err == nil {
		usage.RemainingMicro = avail
	}
	out.CurrentPeriod = nullable.NewNullableWithValue(usage)
	return out
}

// ListPlansOnSale implements GET /plans.
func (s *Server) ListPlansOnSale(ctx context.Context, _ ListPlansOnSaleRequestObject) (ListPlansOnSaleResponseObject, error) {
	rows, err := s.Store.Q.ListPlansOnSale(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]Plan, len(rows))
	for i, p := range rows {
		items[i] = planOf(p)
	}
	return ListPlansOnSale200JSONResponse{Items: items}, nil
}

// PurchasePlan implements POST /plans/{planId}/purchase.
func (s *Server) PurchasePlan(ctx context.Context, req PurchasePlanRequestObject) (PurchasePlanResponseObject, error) {
	auto := false
	if req.Body != nil && req.Body.AutoRenew != nil {
		auto = *req.Body.AutoRenew
	}
	sub, err := s.Subs.Purchase(ctx, currentUser(ctx).ID, req.PlanId, auto)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return PurchasePlan201JSONResponse(s.subscriptionOf(ctx, sub)), nil
}

// ListMySubscriptions implements GET /subscriptions.
func (s *Server) ListMySubscriptions(ctx context.Context, _ ListMySubscriptionsRequestObject) (ListMySubscriptionsResponseObject, error) {
	rows, err := s.Store.Q.ListSubscriptionsByUser(ctx, currentUser(ctx).ID)
	if err != nil {
		return nil, err
	}
	items := make([]Subscription, len(rows))
	for i, sub := range rows {
		items[i] = s.subscriptionOf(ctx, sub)
	}
	return ListMySubscriptions200JSONResponse{Items: items}, nil
}

// RenewSubscription implements POST /subscriptions/{id}/renew.
func (s *Server) RenewSubscription(ctx context.Context, req RenewSubscriptionRequestObject) (RenewSubscriptionResponseObject, error) {
	user := currentUser(ctx).ID
	sub, err := s.Subs.Renew(ctx, &user, req.SubscriptionId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return RenewSubscription200JSONResponse(s.subscriptionOf(ctx, sub)), nil
}

// SetAutoRenew implements PUT /subscriptions/{id}/auto-renew.
func (s *Server) SetAutoRenew(ctx context.Context, req SetAutoRenewRequestObject) (SetAutoRenewResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	sub, err := s.Subs.SetAutoRenew(ctx, currentUser(ctx).ID, req.SubscriptionId, req.Body.AutoRenew)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return SetAutoRenew200JSONResponse(s.subscriptionOf(ctx, sub)), nil
}

// ---- usage ----------------------------------------------------------------------------

func logOf(r sqlc.RequestLog, admin bool) RequestLog {
	out := RequestLog{RequestId: r.RequestID, CreatedAt: r.CreatedAt, UserId: nullUUID(r.UserID), KeyId: nullUUID(r.KeyID),
		AccountId: nullUUID(r.AccountID), Tier: r.Tier, SubscriptionId: nullUUID(r.SubscriptionID), Endpoint: r.Endpoint,
		ModelRaw: r.ModelRaw, Model: r.Model, Stream: r.Stream, StatusCode: int(r.StatusCode), ErrorType: r.ErrorType,
		InputTokens: r.InputTokens, CacheReadTokens: r.CacheReadTokens, CacheWriteTokens: r.CacheWriteTokens,
		OutputTokens: r.OutputTokens, ReasoningTokens: r.ReasoningTokens, UpstreamCostMicro: r.UpstreamCostMicro,
		BilledMicro: r.BilledMicro, TierMultiplier: r.TierMultiplier, ModelMultiplier: r.ModelMultiplier, Unpriced: r.Unpriced,
		UsageMissing: r.UsageMissing, LatencyMs: int(r.LatencyMs), FirstByteMs: int(r.FirstByteMs), Retries: int(r.Retries)}
	ua := r.UserAgent
	out.UserAgent = &ua
	if admin {
		sh, ip := r.SessionHash, r.ClientIp
		out.SessionHash, out.ClientIp = &sh, &ip
	} else {
		// Users do not see which upstream account served them.
		out.AccountId = nullable.NewNullNullable[uuid.UUID]()
	}
	return out
}

type logFilter struct {
	user, key, account *uuid.UUID
	model              *string
	tier               *Tier
	from, to           *time.Time
	cursor             *string
	limit              *int
}

func (s *Server) listLogs(ctx context.Context, f logFilter, admin bool) (RequestLogPage, error) {
	at, id, err := decodeCursor(f.cursor)
	if err != nil {
		return RequestLogPage{}, err
	}
	lim := limitOf(f.limit)
	var tier *string
	if f.tier != nil {
		v := string(*f.tier)
		tier = &v
	}
	var model *string
	if f.model != nil && *f.model != "" {
		v := billing.NormalizeModel(*f.model)
		model = &v
	}
	rows, err := s.Store.Q.ListRequestLogs(ctx, sqlc.ListRequestLogsParams{Limit: lim, UserID: f.user, KeyID: f.key,
		AccountID: f.account, Model: model, Tier: tier, FromAt: f.from, ToAt: f.to, CursorAt: at, CursorID: id})
	if err != nil {
		return RequestLogPage{}, err
	}
	items := make([]RequestLog, len(rows))
	for i, r := range rows {
		items[i] = logOf(r, admin)
	}
	page := RequestLogPage{Items: items, NextCursor: nullable.NewNullNullable[string]()}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = nextCursor(len(rows), lim, last.CreatedAt, last.RequestID)
	}
	return page, nil
}

// ListMyUsageLogs implements GET /usage/logs.
func (s *Server) ListMyUsageLogs(ctx context.Context, req ListMyUsageLogsRequestObject) (ListMyUsageLogsResponseObject, error) {
	user := currentUser(ctx).ID
	p := req.Params
	page, err := s.listLogs(ctx, logFilter{user: &user, key: p.KeyId, model: p.Model, tier: p.Tier, from: p.From, to: p.To,
		cursor: p.Cursor, limit: p.Limit}, false)
	if err != nil {
		return nil, err
	}
	return ListMyUsageLogs200JSONResponse(page), nil
}

func (s *Server) usageSummary(ctx context.Context, gran string, from, to time.Time, user, key, account *uuid.UUID) (UsageSummary, error) {
	if !to.After(from) {
		return UsageSummary{}, badRequest("to must be after from")
	}
	if to.Sub(from) > 400*24*time.Hour {
		return UsageSummary{}, badRequest("range too large (max 400 days)")
	}
	rows, err := s.Store.Q.UsageSummary(ctx, sqlc.UsageSummaryParams{Bucket: gran, FromAt: from, ToAt: to, UserID: user, KeyID: key, AccountID: account})
	if err != nil {
		return UsageSummary{}, err
	}
	items := make([]UsageBucket, len(rows))
	for i, r := range rows {
		items[i] = UsageBucket{Bucket: r.Bucket, Model: r.Model, Tier: r.Tier, Requests: r.Requests, Errors: r.Errors,
			InputTokens: r.InputTokens, CacheReadTokens: r.CacheReadTokens, CacheWriteTokens: r.CacheWriteTokens,
			OutputTokens: r.OutputTokens, ReasoningTokens: r.ReasoningTokens, UpstreamCostMicro: r.UpstreamCostMicro,
			BilledMicro: r.BilledMicro}
	}
	return UsageSummary{Granularity: UsageSummaryGranularity(gran), Items: items}, nil
}

// GetMyUsageSummary implements GET /usage/summary.
func (s *Server) GetMyUsageSummary(ctx context.Context, req GetMyUsageSummaryRequestObject) (GetMyUsageSummaryResponseObject, error) {
	user := currentUser(ctx).ID
	p := req.Params
	out, err := s.usageSummary(ctx, string(p.Granularity), p.From, p.To, &user, p.KeyId, nil)
	if err != nil {
		return nil, err
	}
	return GetMyUsageSummary200JSONResponse(out), nil
}

// ListModels implements GET /models.
func (s *Server) ListModels(ctx context.Context, _ ListModelsRequestObject) (ListModelsResponseObject, error) {
	items := s.Router.ServedModels()
	slices.Sort(items)
	if items == nil {
		items = []string{}
	}
	return ListModels200JSONResponse{Items: items}, nil
}
