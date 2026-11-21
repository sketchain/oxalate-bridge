package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/jobs"
	"github.com/sketchain/oxalate-bridge/backend/internal/router"
	"github.com/sketchain/oxalate-bridge/backend/internal/secret"
	"github.com/sketchain/oxalate-bridge/backend/internal/settings"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
)

// ---- users ----------------------------------------------------------------------

func adminUserOf(u sqlc.User) AdminUser {
	return AdminUser{Id: u.ID, Username: u.Username, Role: Role(u.Role), Status: UserStatus(u.Status), BalanceMicro: u.BalanceMicro,
		MaxConcurrency: int(u.MaxConcurrency), Rpm: nullInt32(u.Rpm), Note: u.Note, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

// AdminListUsers implements GET /admin/users.
func (s *Server) AdminListUsers(ctx context.Context, req AdminListUsersRequestObject) (AdminListUsersResponseObject, error) {
	at, id, err := decodeCursor(req.Params.Cursor)
	if err != nil {
		return nil, err
	}
	lim := limitOf(req.Params.Limit)
	var search *string
	if req.Params.Search != nil && *req.Params.Search != "" {
		search = req.Params.Search
	}
	rows, err := s.Store.Q.ListUsers(ctx, sqlc.ListUsersParams{Limit: lim, Search: search, CursorAt: at, CursorID: id})
	if err != nil {
		return nil, err
	}
	items := make([]AdminUser, len(rows))
	for i, u := range rows {
		items[i] = adminUserOf(u)
	}
	page := AdminUserPage{Items: items, NextCursor: nullable.NewNullNullable[string]()}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = nextCursor(len(rows), lim, last.CreatedAt, last.ID)
	}
	return AdminListUsers200JSONResponse(page), nil
}

// AdminCreateUser implements POST /admin/users.
func (s *Server) AdminCreateUser(ctx context.Context, req AdminCreateUserRequestObject) (AdminCreateUserResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	b := req.Body
	if !usernameRe.MatchString(b.Username) {
		return nil, badRequest("username must be 1-64 characters of letters, digits and _.@-")
	}
	if len(b.Password) < 8 {
		return nil, badRequest("password must have at least 8 characters")
	}
	role := "user"
	if b.Role != nil {
		role = string(*b.Role)
	}
	if role != "user" && role != "admin" {
		return nil, badRequest("unknown role")
	}
	conc := int32(s.Settings.Get().DefaultUserConcurrency)
	if b.MaxConcurrency != nil {
		if *b.MaxConcurrency < 0 {
			return nil, badRequest("max_concurrency must be >= 0")
		}
		conc = int32(*b.MaxConcurrency)
	}
	rpm, err := optInt32(b.Rpm, nil)
	if err != nil {
		return nil, err
	}
	id, _ := uuid.NewV7()
	u, err := s.Store.Q.CreateUser(ctx, sqlc.CreateUserParams{ID: id, Username: b.Username, PasswordHash: secret.HashPassword(b.Password),
		Role: role, MaxConcurrency: conc, Rpm: rpm, Note: deref(b.Note, "")})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	out := adminUserOf(u)
	s.audit(ctx, "user.create", "user", u.ID.String(), nil, out)
	return AdminCreateUser201JSONResponse(out), nil
}

// AdminGetUser implements GET /admin/users/{userId}.
func (s *Server) AdminGetUser(ctx context.Context, req AdminGetUserRequestObject) (AdminGetUserResponseObject, error) {
	u, err := s.Store.Q.GetUser(ctx, req.UserId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return AdminGetUser200JSONResponse(adminUserOf(u)), nil
}

// AdminUpdateUser implements PATCH /admin/users/{userId}.
func (s *Server) AdminUpdateUser(ctx context.Context, req AdminUpdateUserRequestObject) (AdminUpdateUserResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	b := req.Body
	before, err := s.Store.Q.GetUser(ctx, req.UserId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	role := before.Role
	if b.Role != nil {
		role = string(*b.Role)
		if role != "user" && role != "admin" {
			return nil, badRequest("unknown role")
		}
	}
	status := before.Status
	if b.Status != nil {
		status = string(*b.Status)
		if status != "active" && status != "disabled" {
			return nil, badRequest("unknown status")
		}
	}
	if before.ID == currentUser(ctx).ID && (role != "admin" || status != "active") {
		return nil, badRequest("you cannot demote or disable yourself")
	}
	conc := before.MaxConcurrency
	if b.MaxConcurrency != nil {
		if *b.MaxConcurrency < 0 {
			return nil, badRequest("max_concurrency must be >= 0")
		}
		conc = int32(*b.MaxConcurrency)
	}
	rpm, err := optInt32(b.Rpm, before.Rpm)
	if err != nil {
		return nil, err
	}
	if b.Password != nil && len(*b.Password) < 8 {
		return nil, badRequest("password must have at least 8 characters")
	}
	var after sqlc.User
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		after, err = tx.Q.UpdateUser(ctx, sqlc.UpdateUserParams{ID: before.ID, Role: role, Status: status, MaxConcurrency: conc,
			Rpm: rpm, Note: deref(b.Note, before.Note)})
		if err != nil {
			return err
		}
		if b.Password != nil {
			if err := tx.Q.UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{ID: before.ID, PasswordHash: secret.HashPassword(*b.Password)}); err != nil {
				return err
			}
		}
		return tx.InvalidateUser(ctx, before.ID)
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	// A password reset, a disable or a role change signs the user out of
	// every panel session.
	// (An admin resetting their own password keeps the session doing it,
	// like a self-service change.)
	if b.Password != nil || after.Status != before.Status || after.Role != before.Role {
		keep := ""
		if before.ID == currentUser(ctx).ID {
			keep = sessionToken(ctx)
		}
		s.revokeSessions(ctx, before.ID, keep)
	}
	out := adminUserOf(after)
	diffAfter := map[string]any{"user": out, "password_changed": b.Password != nil}
	s.audit(ctx, "user.update", "user", before.ID.String(), adminUserOf(before), diffAfter)
	return AdminUpdateUser200JSONResponse(out), nil
}

// AdminDeleteUser implements DELETE /admin/users/{userId}.
func (s *Server) AdminDeleteUser(ctx context.Context, req AdminDeleteUserRequestObject) (AdminDeleteUserResponseObject, error) {
	if req.UserId == currentUser(ctx).ID {
		return nil, badRequest("you cannot delete yourself")
	}
	before, err := s.Store.Q.GetUser(ctx, req.UserId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.Q.SoftDeleteUser(ctx, before.ID); err != nil {
			return err
		}
		return tx.InvalidateUser(ctx, before.ID)
	})
	if err != nil {
		return nil, err
	}
	s.revokeSessions(ctx, before.ID, "")
	s.audit(ctx, "user.delete", "user", before.ID.String(), adminUserOf(before), nil)
	return AdminDeleteUser204Response{}, nil
}

// AdminAdjustBalance implements POST /admin/users/{userId}/balance.
func (s *Server) AdminAdjustBalance(ctx context.Context, req AdminAdjustBalanceRequestObject) (AdminAdjustBalanceResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	b := req.Body
	if strings.TrimSpace(b.Note) == "" {
		return nil, badRequest("note is required")
	}
	typ := "admin_adjust"
	switch b.Type {
	case Topup:
		typ = "admin_topup"
		if b.AmountMicro <= 0 {
			return nil, badRequest("top-up amount must be positive")
		}
	case Adjust:
		if b.AmountMicro == 0 {
			return nil, badRequest("adjust amount must not be 0")
		}
	default:
		return nil, badRequest("type must be topup or adjust")
	}
	if _, err := s.Store.Q.GetUser(ctx, req.UserId); err != nil {
		return nil, mapDomainErr(err)
	}
	idem := "admin:" + uuid.NewString()
	if b.IdempotencyKey != nil && *b.IdempotencyKey != "" {
		idem = "admin:" + req.UserId.String() + ":" + *b.IdempotencyKey
	}
	op := currentUser(ctx).ID
	var entry sqlc.LedgerEntry
	err := s.Store.InTx(ctx, func(tx *store.Tx) error {
		var inserted bool
		var err error
		entry, inserted, err = tx.AppendLedger(ctx, store.LedgerParams{UserID: req.UserId, Type: typ, Amount: b.AmountMicro,
			RefType: "admin", IdempotencyKey: idem, Note: b.Note, OperatorID: &op, SyncRedis: true})
		if err != nil {
			return err
		}
		if !inserted {
			return conflict("idempotency_key already used")
		}
		return nil
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	out := ledgerOf(entry)
	s.audit(ctx, "user.balance."+string(b.Type), "user", req.UserId.String(), nil, out)
	return AdminAdjustBalance200JSONResponse(out), nil
}

// ---- accounts ------------------------------------------------------------------------

func quotaWindowOf(w *cpa.Window, safety float64, observed, now time.Time) nullable.Nullable[QuotaWindow] {
	eff, _, ok := cpa.EffectiveWindow(w, safety, observed, now)
	if !ok {
		return nullable.NewNullNullable[QuotaWindow]()
	}
	return nullable.NewNullableWithValue(QuotaWindow{UsedPercent: eff.UsedPercent, ResetAt: nullTime(eff.ResetAt),
		TotalUsd: eff.TotalUSD, RemainingUsd: eff.RemainingUSD})
}

func (s *Server) runtimeOf(ctx context.Context, row sqlc.Account, st *router.AccountState, now time.Time) AccountRuntime {
	rt := AccountRuntime{Status: AccountRuntimeStatusNormal, Models: s.Cat.ModelIDs(row.ID),
		CooldownUntil: nullable.NewNullNullable[time.Time](), FiveHour: nullable.NewNullNullable[QuotaWindow](),
		Week: nullable.NewNullNullable[QuotaWindow](), FetchedAt: nullable.NewNullNullable[time.Time](),
		ObservedAt: nullable.NewNullNullable[time.Time]()}
	if rt.Models == nil {
		rt.Models = []string{}
	}
	rt.CurrentConcurrency = s.St.LiveLeases(ctx, s.St.ConcKey("a", row.ID), now)
	if st != nil {
		rt.Stale, rt.HardExhausted = st.Stale, st.HardExhausted
		rt.HeadroomMicro, rt.ReservedMicro, rt.SpendSinceObservedMicro = st.Headroom, st.Reserved, st.Spend
		w := st.Weight
		rt.Weight = &w
		if st.Cooldown != nil {
			rt.CooldownUntil = nullable.NewNullableWithValue(st.Cooldown.Until)
		}
		if snap := st.Snapshot; snap != nil {
			lastErr, dbStatus, loaded := snap.LastError, snap.DatabaseStatus, snap.EstimatesReady
			rt.LastError, rt.DatabaseStatus, rt.EstimatesLoaded = &lastErr, &dbStatus, &loaded
			rt.FetchedAt = nullTime(&snap.FetchedAt)
			if snap.HasData {
				acc := snap.Account
				state, sf := acc.State, acc.SafetyFactor
				rt.State, rt.SafetyFactor = &state, &sf
				rt.ObservedAt = nullTime(acc.ObservedAt)
				observed := snap.FetchedAt
				if acc.ObservedAt != nil {
					observed = *acc.ObservedAt
				}
				rt.FiveHour = quotaWindowOf(acc.FiveHour, acc.SafetyFactor, observed, now)
				rt.Week = quotaWindowOf(acc.Week, acc.SafetyFactor, observed, now)
				if five, err := rt.FiveHour.Get(); err == nil {
					rt.Penalized = five.UsedPercent >= s.effectiveWatermark(row)
				}
			}
		}
	}
	switch {
	case !row.Enabled:
		rt.Status, rt.StatusReason = AccountRuntimeStatusDisabled, "已停用"
	case row.ErrorStatus:
		rt.Status, rt.StatusReason = AccountRuntimeStatusError, row.ErrorReason
	case st != nil && st.Cooldown != nil:
		rt.Status, rt.StatusReason = AccountRuntimeStatusCooldown, st.Cooldown.Reason
	case rt.Stale:
		rt.Status, rt.StatusReason = AccountRuntimeStatusStale, "额度数据过期"
		if rt.LastError != nil && *rt.LastError != "" {
			rt.StatusReason += ": " + *rt.LastError
		}
	case st != nil && st.HardExhausted:
		rt.StatusReason = "额度已用尽（" + st.HardReason + "）"
	}
	return rt
}

// effectiveWatermark is the account's 5-hour watermark (own or global).
func (s *Server) effectiveWatermark(row sqlc.Account) float64 {
	if row.Watermark5hPercent != nil {
		return *row.Watermark5hPercent
	}
	return s.Settings.Get().Watermark5hPercent
}

func (s *Server) accountOf(ctx context.Context, row sqlc.Account, st *router.AccountState, now time.Time) Account {
	wm := s.effectiveWatermark(row)
	out := Account{Id: row.ID, Name: row.Name, BaseUrl: row.BaseUrl, Enabled: row.Enabled,
		Watermark5hPercent: nullFloat(row.Watermark5hPercent), Watermark5hPercentEffective: wm,
		MaxConcurrency: nullInt32(row.MaxConcurrency), Note: row.Note, Instance: row.Instance, Subscription: row.Subscription,
		Tier: row.Tier, AuthIndex: row.AuthIndex, Provider: row.Provider, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Runtime: s.runtimeOf(ctx, row, st, now), WeekTotalMicro: nullable.NewNullNullable[int64]()}
	s.fillSold(ctx, &out, st)
	return out
}

func (s *Server) accountStates(ctx context.Context, ids ...uuid.UUID) map[uuid.UUID]*router.AccountState {
	var accounts []*router.Account
	if len(ids) > 0 {
		for _, id := range ids {
			if a, ok := s.Cat.Account(id); ok {
				accounts = append(accounts, a)
			}
		}
		if len(accounts) == 0 {
			return map[uuid.UUID]*router.AccountState{}
		}
	}
	states, err := s.Router.States(ctx, accounts, time.Now().UTC())
	if err != nil {
		s.Log.Warn("account states", "err", err)
		return map[uuid.UUID]*router.AccountState{}
	}
	return states
}

// validation is the outcome of checking an account connection.
type validation struct {
	result      AccountTestResult
	quota       *cpa.QuotaResponse
	models      []string
	unsupported string // the non-claude subscription /quota reported
}

func (s *Server) validateAccount(ctx context.Context, baseURL, cpaKey, quotaToken string) validation {
	var v validation
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	q, err := s.CPA.Quota(cctx, baseURL, quotaToken)
	if err != nil {
		msg := err.Error()
		v.result.QuotaError = &msg
	} else {
		acc := q.Accounts[0]
		v.result.Instance, v.result.Subscription, v.result.Tier = &q.Instance, &q.Subscription, &q.Tier
		v.result.AuthIndex, v.result.Provider = &acc.AuthIndex, &acc.Provider
		if !cpa.IsClaude(q) {
			// Only Claude subscriptions are served.
			v.unsupported = q.Subscription
			msg := fmt.Sprintf("unsupported subscription %q: only claude accounts can be used", q.Subscription)
			v.result.QuotaError = &msg
		} else {
			v.result.QuotaOk, v.quota = true, q
		}
	}
	models, err := s.CPA.Models(cctx, baseURL, cpaKey)
	if err != nil {
		msg := err.Error()
		v.result.ModelsError = &msg
	} else {
		v.result.ModelsOk, v.models = true, models
		v.result.Models = &models
	}
	v.result.Ok = v.result.QuotaOk && v.result.ModelsOk
	return v
}

func (v validation) err() error {
	if v.result.Ok {
		return nil
	}
	if v.unsupported != "" {
		return apiErr(http.StatusUnprocessableEntity, "unsupported_subscription", *v.result.QuotaError)
	}
	var parts []string
	if v.result.QuotaError != nil {
		parts = append(parts, "/quota: "+*v.result.QuotaError)
	}
	if v.result.ModelsError != nil {
		parts = append(parts, "/v1/models: "+*v.result.ModelsError)
	}
	return upstreamInvalid(strings.Join(parts, "; "))
}

// storeValidation seeds Redis with the snapshot and models just fetched so
// the account is routable at once.
func (s *Server) storeValidation(ctx context.Context, id uuid.UUID, v validation) {
	now := time.Now().UTC()
	if v.quota != nil {
		snap := cpa.Snapshot{FetchedAt: now, LastAttemptAt: now, DatabaseStatus: v.quota.Database.Status,
			EstimatesReady: v.quota.Estimates.Loaded, Instance: v.quota.Instance, Subscription: v.quota.Subscription,
			Tier: v.quota.Tier, Account: v.quota.Accounts[0], HasData: true}
		raw, _ := json.Marshal(snap)
		_ = s.St.R.Set(ctx, s.St.QuotaKey(id), raw, 0).Err()
	}
	if v.models != nil {
		raw, _ := json.Marshal(v.models)
		_ = s.St.R.Set(ctx, s.St.ModelsKey(id), raw, 0).Err()
	}
}

func (s *Server) reloadCatalog(ctx context.Context) {
	if err := s.Cat.ReloadDB(ctx); err != nil {
		s.Log.Warn("reload catalog", "err", err)
	}
	_ = s.Cat.ReloadRedis(ctx)
}

// AdminListAccounts implements GET /admin/accounts.
func (s *Server) AdminListAccounts(ctx context.Context, _ AdminListAccountsRequestObject) (AdminListAccountsResponseObject, error) {
	rows, err := s.Store.Q.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	states := s.accountStates(ctx)
	now := time.Now().UTC()
	items := make([]Account, len(rows))
	for i, row := range rows {
		items[i] = s.accountOf(ctx, row, states[row.ID], now)
	}
	s.fillK(ctx, items)
	return AdminListAccounts200JSONResponse{Items: items}, nil
}

// AdminGetAccount implements GET /admin/accounts/{accountId}.
func (s *Server) AdminGetAccount(ctx context.Context, req AdminGetAccountRequestObject) (AdminGetAccountResponseObject, error) {
	row, err := s.Store.Q.GetAccount(ctx, req.AccountId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	states := s.accountStates(ctx, row.ID)
	items := []Account{s.accountOf(ctx, row, states[row.ID], time.Now().UTC())}
	s.fillK(ctx, items)
	return AdminGetAccount200JSONResponse(items[0]), nil
}

func checkAccountNumbers(wm *float64, conc *int32) error {
	if wm != nil && (*wm < 0 || *wm > 100) {
		return badRequest("watermark_5h_percent must be within 0..100")
	}
	if conc != nil && *conc < 0 {
		return badRequest("max_concurrency must be >= 0")
	}
	return nil
}

// accountAudit is the audit form of an account (no secrets).
func accountAudit(row sqlc.Account) map[string]any {
	return map[string]any{"name": row.Name, "base_url": row.BaseUrl, "enabled": row.Enabled,
		"watermark_5h_percent": row.Watermark5hPercent, "max_concurrency": row.MaxConcurrency, "note": row.Note,
		"instance": row.Instance, "subscription": row.Subscription, "tier": row.Tier, "auth_index": row.AuthIndex}
}

// AdminCreateAccount implements POST /admin/accounts.
func (s *Server) AdminCreateAccount(ctx context.Context, req AdminCreateAccountRequestObject) (AdminCreateAccountResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	b := req.Body
	if strings.TrimSpace(b.Name) == "" || b.BaseUrl == "" || b.CpaKey == "" || b.QuotaToken == "" {
		return nil, badRequest("name, base_url, cpa_key and quota_token are required")
	}
	wm := optFloat(b.Watermark5hPercent, nil)
	conc, err := optInt32(b.MaxConcurrency, nil)
	if err != nil {
		return nil, err
	}
	if err := checkAccountNumbers(wm, conc); err != nil {
		return nil, err
	}
	v := s.validateAccount(ctx, b.BaseUrl, b.CpaKey, b.QuotaToken)
	if err := v.err(); err != nil {
		return nil, err
	}
	acc := v.quota.Accounts[0]
	id, _ := uuid.NewV7()
	var row sqlc.Account
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		row, err = tx.Q.CreateAccount(ctx, sqlc.CreateAccountParams{ID: id, Name: strings.TrimSpace(b.Name),
			BaseUrl: strings.TrimRight(b.BaseUrl, "/"), CpaKeyEnc: s.Box.Seal(b.CpaKey), QuotaTokenEnc: s.Box.Seal(b.QuotaToken),
			Enabled: deref(b.Enabled, true), Watermark5hPercent: wm, MaxConcurrency: conc, Note: deref(b.Note, ""),
			Instance: v.quota.Instance, Subscription: v.quota.Subscription, Tier: v.quota.Tier, AuthIndex: acc.AuthIndex, Provider: acc.Provider})
		if err != nil {
			return err
		}
		return tx.RoutingChanged(ctx)
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	s.storeValidation(ctx, row.ID, v)
	s.reloadCatalog(ctx)
	s.audit(ctx, "account.create", "account", row.ID.String(), nil, accountAudit(row))
	states := s.accountStates(ctx, row.ID)
	return AdminCreateAccount201JSONResponse(s.accountOf(ctx, row, states[row.ID], time.Now().UTC())), nil
}

// AdminUpdateAccount implements PATCH /admin/accounts/{accountId}.
func (s *Server) AdminUpdateAccount(ctx context.Context, req AdminUpdateAccountRequestObject) (AdminUpdateAccountResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	b := req.Body
	before, err := s.Store.Q.GetAccount(ctx, req.AccountId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	cpaKey, err := s.Box.Open(before.CpaKeyEnc)
	if err != nil {
		return nil, err
	}
	quotaToken, err := s.Box.Open(before.QuotaTokenEnc)
	if err != nil {
		return nil, err
	}
	if b.CpaKey != nil {
		cpaKey = *b.CpaKey
	}
	if b.QuotaToken != nil {
		quotaToken = *b.QuotaToken
	}
	baseURL := strings.TrimRight(deref(b.BaseUrl, before.BaseUrl), "/")
	wm := optFloat(b.Watermark5hPercent, before.Watermark5hPercent)
	conc, err := optInt32(b.MaxConcurrency, before.MaxConcurrency)
	if err != nil {
		return nil, err
	}
	if err := checkAccountNumbers(wm, conc); err != nil {
		return nil, err
	}
	v := s.validateAccount(ctx, baseURL, cpaKey, quotaToken)
	if err := v.err(); err != nil {
		return nil, err
	}
	acc := v.quota.Accounts[0]
	var after sqlc.Account
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		after, err = tx.Q.UpdateAccount(ctx, sqlc.UpdateAccountParams{ID: before.ID, Name: strings.TrimSpace(deref(b.Name, before.Name)),
			BaseUrl: baseURL, CpaKeyEnc: s.Box.Seal(cpaKey), QuotaTokenEnc: s.Box.Seal(quotaToken), Enabled: deref(b.Enabled, before.Enabled),
			Watermark5hPercent: wm, MaxConcurrency: conc, Note: deref(b.Note, before.Note), Instance: v.quota.Instance,
			Subscription: v.quota.Subscription, Tier: v.quota.Tier, AuthIndex: acc.AuthIndex, Provider: acc.Provider})
		if err != nil {
			return err
		}
		return tx.RoutingChanged(ctx)
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	_ = s.St.ClearCooldown(ctx, after.ID)
	s.storeValidation(ctx, after.ID, v)
	s.reloadCatalog(ctx)
	diff := accountAudit(after)
	diff["cpa_key_changed"], diff["quota_token_changed"] = b.CpaKey != nil, b.QuotaToken != nil
	s.audit(ctx, "account.update", "account", after.ID.String(), accountAudit(before), diff)
	states := s.accountStates(ctx, after.ID)
	return AdminUpdateAccount200JSONResponse(s.accountOf(ctx, after, states[after.ID], time.Now().UTC())), nil
}

// accountHasSubscriptions is the 409 returned when deleting an account that
// active or pending subscriptions are bound to.
func accountHasSubscriptions(rows []sqlc.ListBlockingSubscriptionsByAccountRow) error {
	subs := make([]BlockingSubscription, len(rows))
	for i, r := range rows {
		subs[i] = BlockingSubscription{Id: r.ID, UserId: r.UserID, Username: r.Username, Status: SubscriptionStatus(r.Status), PlanName: r.PlanName}
	}
	e := apiErr(http.StatusConflict, "account_has_subscriptions",
		fmt.Sprintf("%d active or pending subscription(s) are bound to this account; rebind or cancel them first (disabling the account is allowed)", len(rows)))
	e.Details = map[string]any{"subscriptions": subs}
	return e
}

// AdminDeleteAccount implements DELETE /admin/accounts/{accountId}.
// Accounts with active or pending_assignment subscriptions cannot be deleted.
func (s *Server) AdminDeleteAccount(ctx context.Context, req AdminDeleteAccountRequestObject) (AdminDeleteAccountResponseObject, error) {
	before, err := s.Store.Q.GetAccount(ctx, req.AccountId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		// The row lock serializes against subscription binds (they share-lock
		// the account), so the check below cannot miss a concurrent bind.
		if _, err := tx.Q.LockAccountForUpdate(ctx, before.ID); err != nil {
			return err
		}
		blocking, err := tx.Q.ListBlockingSubscriptionsByAccount(ctx, &before.ID)
		if err != nil {
			return err
		}
		if len(blocking) > 0 {
			return accountHasSubscriptions(blocking)
		}
		if err := tx.Q.SoftDeleteAccount(ctx, before.ID); err != nil {
			return err
		}
		return tx.RoutingChanged(ctx)
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	s.reloadCatalog(ctx)
	s.audit(ctx, "account.delete", "account", before.ID.String(), accountAudit(before), nil)
	return AdminDeleteAccount204Response{}, nil
}

// AdminSetAccountEnabled implements PUT /admin/accounts/{accountId}/enabled.
func (s *Server) AdminSetAccountEnabled(ctx context.Context, req AdminSetAccountEnabledRequestObject) (AdminSetAccountEnabledResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	before, err := s.Store.Q.GetAccount(ctx, req.AccountId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	var after sqlc.Account
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		after, err = tx.Q.SetAccountEnabled(ctx, sqlc.SetAccountEnabledParams{ID: before.ID, Enabled: req.Body.Enabled})
		if err != nil {
			return err
		}
		return tx.RoutingChanged(ctx)
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if req.Body.Enabled {
		_ = s.St.ClearCooldown(ctx, after.ID)
	}
	s.reloadCatalog(ctx)
	s.audit(ctx, "account.set_enabled", "account", after.ID.String(), map[string]any{"enabled": before.Enabled},
		map[string]any{"enabled": after.Enabled})
	states := s.accountStates(ctx, after.ID)
	return AdminSetAccountEnabled200JSONResponse(s.accountOf(ctx, after, states[after.ID], time.Now().UTC())), nil
}

// AdminTestAccount implements POST /admin/accounts/{accountId}/test.
func (s *Server) AdminTestAccount(ctx context.Context, req AdminTestAccountRequestObject) (AdminTestAccountResponseObject, error) {
	row, err := s.Store.Q.GetAccount(ctx, req.AccountId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	cpaKey, err := s.Box.Open(row.CpaKeyEnc)
	if err != nil {
		return nil, err
	}
	quotaToken, err := s.Box.Open(row.QuotaTokenEnc)
	if err != nil {
		return nil, err
	}
	v := s.validateAccount(ctx, row.BaseUrl, cpaKey, quotaToken)
	if v.result.Ok {
		err := s.Store.InTx(ctx, func(tx *store.Tx) error {
			if err := tx.Q.ClearAccountError(ctx, row.ID); err != nil {
				return err
			}
			acc := v.quota.Accounts[0]
			if err := tx.Q.UpdateAccountQuotaInfo(ctx, sqlc.UpdateAccountQuotaInfoParams{ID: row.ID, Instance: v.quota.Instance,
				Subscription: v.quota.Subscription, Tier: v.quota.Tier, AuthIndex: acc.AuthIndex, Provider: acc.Provider}); err != nil {
				return err
			}
			return tx.RoutingChanged(ctx)
		})
		if err != nil {
			return nil, err
		}
		_ = s.St.ClearCooldown(ctx, row.ID)
		s.storeValidation(ctx, row.ID, v)
		s.reloadCatalog(ctx)
	}
	s.audit(ctx, "account.test", "account", row.ID.String(), nil, map[string]any{"ok": v.result.Ok})
	return AdminTestAccount200JSONResponse(v.result), nil
}

// ---- plans ---------------------------------------------------------------------------

func planParams(b *PlanInput) (sqlc.CreatePlanParams, error) {
	if b == nil {
		return sqlc.CreatePlanParams{}, badRequest("body required")
	}
	if strings.TrimSpace(b.Name) == "" {
		return sqlc.CreatePlanParams{}, badRequest("name is required")
	}
	if b.Subscription != Claude {
		return sqlc.CreatePlanParams{}, apiErr(http.StatusBadRequest, "unsupported_subscription", "only claude plans are supported")
	}
	if b.WeeklyQuotaMicro < 0 || b.PriceMicro < 0 || b.DurationValue < 1 {
		return sqlc.CreatePlanParams{}, badRequest("quota and price must be >= 0 and duration >= 1")
	}
	switch b.DurationUnit {
	case DurationUnitDay, DurationUnitWeek, DurationUnitMonth:
	default:
		return sqlc.CreatePlanParams{}, badRequest("duration_unit must be day, week or month")
	}
	models := []string{}
	seen := map[string]bool{}
	if b.AllowedModels != nil {
		for _, m := range *b.AllowedModels {
			n := billing.NormalizeModel(m)
			if n != "" && !seen[n] {
				seen[n] = true
				models = append(models, n)
			}
		}
	}
	return sqlc.CreatePlanParams{Name: strings.TrimSpace(b.Name), Description: deref(b.Description, ""), Subscription: string(b.Subscription),
		WeeklyQuotaMicro: b.WeeklyQuotaMicro, PriceMicro: b.PriceMicro, DurationValue: int32(b.DurationValue),
		DurationUnit: string(b.DurationUnit), AllowedModels: models, OnSale: deref(b.OnSale, true), SortOrder: int32(deref(b.SortOrder, 0))}, nil
}

// AdminListPlans implements GET /admin/plans.
func (s *Server) AdminListPlans(ctx context.Context, _ AdminListPlansRequestObject) (AdminListPlansResponseObject, error) {
	rows, err := s.Store.Q.ListPlans(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]Plan, len(rows))
	for i, p := range rows {
		items[i] = planOf(p)
	}
	return AdminListPlans200JSONResponse{Items: items}, nil
}

// AdminCreatePlan implements POST /admin/plans.
func (s *Server) AdminCreatePlan(ctx context.Context, req AdminCreatePlanRequestObject) (AdminCreatePlanResponseObject, error) {
	p, err := planParams(req.Body)
	if err != nil {
		return nil, err
	}
	p.ID, _ = uuid.NewV7()
	row, err := s.Store.Q.CreatePlan(ctx, p)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	out := planOf(row)
	s.audit(ctx, "plan.create", "plan", row.ID.String(), nil, out)
	return AdminCreatePlan201JSONResponse(out), nil
}

// AdminGetPlan implements GET /admin/plans/{planId}.
func (s *Server) AdminGetPlan(ctx context.Context, req AdminGetPlanRequestObject) (AdminGetPlanResponseObject, error) {
	row, err := s.Store.Q.GetPlan(ctx, req.PlanId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return AdminGetPlan200JSONResponse(planOf(row)), nil
}

// AdminUpdatePlan implements PUT /admin/plans/{planId}.
func (s *Server) AdminUpdatePlan(ctx context.Context, req AdminUpdatePlanRequestObject) (AdminUpdatePlanResponseObject, error) {
	p, err := planParams(req.Body)
	if err != nil {
		return nil, err
	}
	before, err := s.Store.Q.GetPlan(ctx, req.PlanId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	row, err := s.Store.Q.UpdatePlan(ctx, sqlc.UpdatePlanParams{ID: before.ID, Name: p.Name, Description: p.Description,
		Subscription: p.Subscription, WeeklyQuotaMicro: p.WeeklyQuotaMicro, PriceMicro: p.PriceMicro, DurationValue: p.DurationValue,
		DurationUnit: p.DurationUnit, AllowedModels: p.AllowedModels, OnSale: p.OnSale, SortOrder: p.SortOrder})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	out := planOf(row)
	s.audit(ctx, "plan.update", "plan", row.ID.String(), planOf(before), out)
	return AdminUpdatePlan200JSONResponse(out), nil
}

// AdminDeletePlan implements DELETE /admin/plans/{planId}.
func (s *Server) AdminDeletePlan(ctx context.Context, req AdminDeletePlanRequestObject) (AdminDeletePlanResponseObject, error) {
	before, err := s.Store.Q.GetPlan(ctx, req.PlanId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if err := s.Store.Q.SoftDeletePlan(ctx, before.ID); err != nil {
		return nil, err
	}
	s.audit(ctx, "plan.delete", "plan", before.ID.String(), planOf(before), nil)
	return AdminDeletePlan204Response{}, nil
}

// ---- subscriptions ----------------------------------------------------------------------

// AdminListSubscriptions implements GET /admin/subscriptions.
func (s *Server) AdminListSubscriptions(ctx context.Context, req AdminListSubscriptionsRequestObject) (AdminListSubscriptionsResponseObject, error) {
	at, id, err := decodeCursor(req.Params.Cursor)
	if err != nil {
		return nil, err
	}
	lim := limitOf(req.Params.Limit)
	var status *string
	if req.Params.Status != nil {
		v := string(*req.Params.Status)
		status = &v
	}
	rows, err := s.Store.Q.ListSubscriptions(ctx, sqlc.ListSubscriptionsParams{Limit: lim, Status: status, UserID: req.Params.UserId,
		AccountID: req.Params.AccountId, CursorAt: at, CursorID: id})
	if err != nil {
		return nil, err
	}
	items := s.adminSubscriptions(ctx, rows)
	page := SubscriptionPage{Items: items, NextCursor: nullable.NewNullNullable[string]()}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = nextCursor(len(rows), lim, last.CreatedAt, last.ID)
	}
	return AdminListSubscriptions200JSONResponse(page), nil
}

func (s *Server) withWarning(ctx context.Context, sub sqlc.PlanSubscription, warning string) SubscriptionWithWarning {
	out := SubscriptionWithWarning{Subscription: s.adminSubscriptions(ctx, []sqlc.PlanSubscription{sub})[0],
		Warning: nullable.NewNullNullable[string]()}
	if warning != "" {
		out.Warning = nullable.NewNullableWithValue(warning)
	}
	return out
}

// AdminGrantSubscription implements POST /admin/subscriptions.
func (s *Server) AdminGrantSubscription(ctx context.Context, req AdminGrantSubscriptionRequestObject) (AdminGrantSubscriptionResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	b := req.Body
	if _, err := s.Store.Q.GetUser(ctx, b.UserId); err != nil {
		return nil, mapDomainErr(err)
	}
	sub, warning, err := s.Subs.Grant(ctx, currentUser(ctx).ID, b.UserId, b.PlanId, b.AccountId, deref(b.Charge, false))
	if err != nil {
		return nil, mapDomainErr(err)
	}
	s.reloadCatalog(ctx)
	out := s.withWarning(ctx, sub, warning)
	s.audit(ctx, "subscription.grant", "subscription", sub.ID.String(), nil, out)
	return AdminGrantSubscription201JSONResponse(out), nil
}

// AdminAssignSubscription implements POST /admin/subscriptions/{id}/assign.
func (s *Server) AdminAssignSubscription(ctx context.Context, req AdminAssignSubscriptionRequestObject) (AdminAssignSubscriptionResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	sub, warning, err := s.Subs.Assign(ctx, req.SubscriptionId, req.Body.AccountId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	s.reloadCatalog(ctx)
	out := s.withWarning(ctx, sub, warning)
	s.audit(ctx, "subscription.assign", "subscription", sub.ID.String(), nil, out)
	return AdminAssignSubscription200JSONResponse(out), nil
}

// AdminRebindSubscription implements POST /admin/subscriptions/{id}/rebind.
func (s *Server) AdminRebindSubscription(ctx context.Context, req AdminRebindSubscriptionRequestObject) (AdminRebindSubscriptionResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	before, err := s.Store.Q.GetSubscription(ctx, req.SubscriptionId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	sub, warning, err := s.Subs.Rebind(ctx, req.SubscriptionId, req.Body.AccountId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	s.reloadCatalog(ctx)
	out := s.withWarning(ctx, sub, warning)
	s.audit(ctx, "subscription.rebind", "subscription", sub.ID.String(), map[string]any{"account_id": before.AccountID},
		map[string]any{"account_id": sub.AccountID})
	return AdminRebindSubscription200JSONResponse(out), nil
}

// AdminCancelSubscription implements POST /admin/subscriptions/{id}/cancel.
func (s *Server) AdminCancelSubscription(ctx context.Context, req AdminCancelSubscriptionRequestObject) (AdminCancelSubscriptionResponseObject, error) {
	if req.Body == nil || req.Body.RefundMicro < 0 {
		return nil, badRequest("refund_micro must be >= 0")
	}
	sub, err := s.Subs.Cancel(ctx, currentUser(ctx).ID, req.SubscriptionId, req.Body.RefundMicro, deref(req.Body.Note, ""))
	if err != nil {
		return nil, mapDomainErr(err)
	}
	s.reloadCatalog(ctx)
	out := s.adminSubscriptions(ctx, []sqlc.PlanSubscription{sub})[0]
	s.audit(ctx, "subscription.cancel", "subscription", sub.ID.String(), nil,
		map[string]any{"refund_micro": req.Body.RefundMicro, "refund": fmtMicro(req.Body.RefundMicro)})
	return AdminCancelSubscription200JSONResponse(out), nil
}

// ---- settings ----------------------------------------------------------------------------

func settingsOf(v settings.Values) (Settings, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return Settings{}, err
	}
	var out Settings
	return out, json.Unmarshal(raw, &out)
}

// AdminGetSettings implements GET /admin/settings.
func (s *Server) AdminGetSettings(ctx context.Context, _ AdminGetSettingsRequestObject) (AdminGetSettingsResponseObject, error) {
	out, err := settingsOf(s.Settings.Get())
	if err != nil {
		return nil, err
	}
	return AdminGetSettings200JSONResponse(out), nil
}

// AdminUpdateSettings implements PATCH /admin/settings.
func (s *Server) AdminUpdateSettings(ctx context.Context, req AdminUpdateSettingsRequestObject) (AdminUpdateSettingsResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	if req.Body.ModelMultipliers != nil {
		norm := map[string]float64{}
		for k, v := range *req.Body.ModelMultipliers {
			if n := billing.NormalizeModel(k); n != "" {
				norm[n] = v
			}
		}
		req.Body.ModelMultipliers = &norm
	}
	raw, err := json.Marshal(req.Body)
	if err != nil {
		return nil, err
	}
	patch := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &patch); err != nil {
		return nil, err
	}
	before := s.Settings.Get()
	next, changed, err := settings.Patch(before, patch)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	nextMap, err := settings.ToMap(next)
	if err != nil {
		return nil, err
	}
	op := currentUser(ctx).ID
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		for _, k := range changed {
			if err := tx.Q.UpsertSetting(ctx, sqlc.UpsertSettingParams{Key: k, Value: nextMap[k], UpdatedBy: &op}); err != nil {
				return err
			}
		}
		return tx.Enqueue(ctx, store.OutboxSettings, map[string]any{"keys": changed})
	})
	if err != nil {
		return nil, err
	}
	s.Settings.Set(next)
	beforeOut, _ := settingsOf(before)
	out, err := settingsOf(next)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "settings.update", "settings", strings.Join(changed, ","), beforeOut, out)
	return AdminUpdateSettings200JSONResponse(out), nil
}

// ---- logs, ledger, audit, safeguard ---------------------------------------------------------

// AdminListRequestLogs implements GET /admin/request-logs.
func (s *Server) AdminListRequestLogs(ctx context.Context, req AdminListRequestLogsRequestObject) (AdminListRequestLogsResponseObject, error) {
	p := req.Params
	page, err := s.listLogs(ctx, logFilter{user: p.UserId, key: p.KeyId, account: p.AccountId, model: p.Model, tier: p.Tier,
		status: p.StatusCode, from: p.From, to: p.To, cursor: p.Cursor, limit: p.Limit})
	if err != nil {
		return nil, err
	}
	return AdminListRequestLogs200JSONResponse(page), nil
}

// AdminGetUsageSummary implements GET /admin/usage/summary.
func (s *Server) AdminGetUsageSummary(ctx context.Context, req AdminGetUsageSummaryRequestObject) (AdminGetUsageSummaryResponseObject, error) {
	p := req.Params
	out, err := s.usageSummary(ctx, string(p.Granularity), p.From, p.To, p.UserId, p.KeyId, p.AccountId)
	if err != nil {
		return nil, err
	}
	return AdminGetUsageSummary200JSONResponse(out), nil
}

// AdminListLedger implements GET /admin/ledger.
func (s *Server) AdminListLedger(ctx context.Context, req AdminListLedgerRequestObject) (AdminListLedgerResponseObject, error) {
	page, err := s.listLedger(ctx, req.Params.UserId, req.Params.Type, req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	return AdminListLedger200JSONResponse(page), nil
}

// AdminListAuditLogs implements GET /admin/audit-logs.
func (s *Server) AdminListAuditLogs(ctx context.Context, req AdminListAuditLogsRequestObject) (AdminListAuditLogsResponseObject, error) {
	at, id, err := decodeCursor(req.Params.Cursor)
	if err != nil {
		return nil, err
	}
	lim := limitOf(req.Params.Limit)
	rows, err := s.Store.Q.ListAuditLogs(ctx, sqlc.ListAuditLogsParams{Limit: lim, OperatorID: req.Params.OperatorId,
		TargetType: req.Params.TargetType, CursorAt: at, CursorID: id})
	if err != nil {
		return nil, err
	}
	items := make([]AuditLog, len(rows))
	for i, r := range rows {
		diff := map[string]any{}
		_ = json.Unmarshal(r.Diff, &diff)
		items[i] = AuditLog{Id: r.ID, OperatorId: nullUUID(r.OperatorID), Action: r.Action, TargetType: r.TargetType,
			TargetId: r.TargetID, Diff: diff, Ip: r.Ip, CreatedAt: r.CreatedAt}
	}
	page := AuditLogPage{Items: items, NextCursor: nullable.NewNullNullable[string]()}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = nextCursor(len(rows), lim, last.CreatedAt, last.ID)
	}
	return AdminListAuditLogs200JSONResponse(page), nil
}

// AdminListSafeguardEvents implements GET /admin/safeguard-events.
func (s *Server) AdminListSafeguardEvents(ctx context.Context, req AdminListSafeguardEventsRequestObject) (AdminListSafeguardEventsResponseObject, error) {
	at, id, err := decodeCursor(req.Params.Cursor)
	if err != nil {
		return nil, err
	}
	lim := limitOf(req.Params.Limit)
	rows, err := s.Store.Q.ListSafeguardEvents(ctx, sqlc.ListSafeguardEventsParams{Limit: lim, UserID: req.Params.UserId, CursorAt: at, CursorID: id})
	if err != nil {
		return nil, err
	}
	items := make([]SafeguardEvent, len(rows))
	for i, r := range rows {
		items[i] = SafeguardEvent{Id: r.ID, RequestId: r.RequestID, UserId: nullUUID(r.UserID), KeyId: nullUUID(r.KeyID),
			AccountId: nullUUID(r.AccountID), Model: r.Model, Category: r.Category, Reason: r.Reason, CreatedAt: r.CreatedAt}
	}
	page := SafeguardEventPage{Items: items, NextCursor: nullable.NewNullNullable[string]()}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = nextCursor(len(rows), lim, last.CreatedAt, last.ID)
	}
	return AdminListSafeguardEvents200JSONResponse(page), nil
}

// ---- prices, reconcile, events ----------------------------------------------------------------

func (s *Server) priceStatus() PriceStatus {
	st := s.Prices.Status()
	return PriceStatus{SourceUrl: st.SourceURL, Origin: PriceStatusOrigin(st.Origin), Models: st.Models,
		FetchedAt: nullTime(&st.FetchedAt), LastAttemptAt: nullTime(&st.LastAttemptAt), LastError: st.LastError}
}

// AdminGetPrices implements GET /admin/prices.
func (s *Server) AdminGetPrices(ctx context.Context, _ AdminGetPricesRequestObject) (AdminGetPricesResponseObject, error) {
	return AdminGetPrices200JSONResponse(s.priceStatus()), nil
}

// AdminRefreshPrices implements POST /admin/prices/refresh.
func (s *Server) AdminRefreshPrices(ctx context.Context, _ AdminRefreshPricesRequestObject) (AdminRefreshPricesResponseObject, error) {
	if s.RefreshPrices == nil {
		return nil, errors.New("price refresh unavailable")
	}
	err := s.RefreshPrices(ctx)
	s.audit(ctx, "prices.refresh", "prices", "", nil, map[string]any{"ok": err == nil})
	if err != nil {
		return nil, apiErr(http.StatusBadGateway, "upstream_validation_failed", fmt.Sprintf("refresh failed, old table kept: %v", err))
	}
	return AdminRefreshPrices200JSONResponse(s.priceStatus()), nil
}

// AdminRunReconcile implements POST /admin/reconcile.
func (s *Server) AdminRunReconcile(ctx context.Context, _ AdminRunReconcileRequestObject) (AdminRunReconcileResponseObject, error) {
	res, err := jobs.Reconcile(ctx, s.Store, s.Log)
	if err != nil {
		return nil, err
	}
	out := ReconcileResult{Ok: res.OK, BilledTotalMicro: res.BilledTotal, ApiChargeTotalMicro: res.ChargeTotal}
	out.BalanceMismatches = make([]struct {
		BalanceMicro     int64     `json:"balance_micro"`
		LedgerTotalMicro int64     `json:"ledger_total_micro"`
		UserId           uuid.UUID `json:"user_id"`
	}, len(res.BalanceMismatches))
	for i, m := range res.BalanceMismatches {
		out.BalanceMismatches[i].UserId, out.BalanceMismatches[i].BalanceMicro, out.BalanceMismatches[i].LedgerTotalMicro =
			m.ID, m.BalanceMicro, m.LedgerTotal
	}
	s.audit(ctx, "reconcile.run", "reconcile", "", nil, map[string]any{"ok": res.OK})
	return AdminRunReconcile200JSONResponse(out), nil
}

// sseResponse streams admin events until the client leaves.
type sseResponse struct {
	s   *Server
	ctx context.Context
}

func (r sseResponse) VisitAdminEventsResponse(w http.ResponseWriter) error {
	ch, unsubscribe := r.s.Hub.Subscribe()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_, _ = fmt.Fprint(w, ": connected\n\n")
	_ = rc.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return nil
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return nil
			}
		case ev := <-ch:
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, ev.Data); err != nil {
				return nil
			}
		}
		if err := rc.Flush(); err != nil {
			return nil
		}
	}
}

// AdminEvents implements GET /admin/events.
func (s *Server) AdminEvents(ctx context.Context, _ AdminEventsRequestObject) (AdminEventsResponseObject, error) {
	return sseResponse{s: s, ctx: ctx}, nil
}

var _ = errors.Is(nil, pgx.ErrNoRows)
