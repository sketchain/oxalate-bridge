package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/keymeter"
	"github.com/sketchain/oxalate-bridge/backend/internal/router"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/secret"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
)

// ---- keys -----------------------------------------------------------------------

const maxAllowlist = 100

func nullStrings(v []string) nullable.Nullable[[]string] {
	if len(v) == 0 {
		return nullable.NewNullNullable[[]string]()
	}
	return nullable.NewNullableWithValue(v)
}

// keyStatus is active, disabled or expired.
func keyStatus(enabled, userActive bool, expires *time.Time, now time.Time) KeyStatusValue {
	switch {
	case expires != nil && !now.Before(*expires):
		return KeyStatusValueExpired
	case !enabled || !userActive:
		return KeyStatusValueDisabled
	}
	return KeyStatusValueActive
}

func keyOf(k sqlc.ApiKey) ApiKey {
	tiers := make([]Tier, len(k.Tiers))
	for i, t := range k.Tiers {
		tiers[i] = Tier(t)
	}
	out := ApiKey{Id: k.ID, Name: k.Name, Prefix: k.KeyPrefix, Tiers: tiers, SubscriptionId: nullUUID(k.SubscriptionID),
		MaxConcurrency: nullInt32(k.MaxConcurrency), Rpm: nullInt32(k.Rpm), Enabled: k.Enabled, Note: k.Note,
		LastUsedAt: nullTime(k.LastUsedAt), CreatedAt: k.CreatedAt,
		Status:          keyStatus(k.Enabled, true, k.ExpiresAt, time.Now()),
		QuotaLimitMicro: nullInt64(k.QuotaLimitMicro), QuotaScope: QuotaScope(k.QuotaScope),
		Limit5hMicro: nullInt64(k.Limit5hMicro), Limit1dMicro: nullInt64(k.Limit1dMicro), Limit7dMicro: nullInt64(k.Limit7dMicro),
		ExpiresAt: nullTime(k.ExpiresAt), ModelAllowlist: nullStrings(k.ModelAllowlist), IpAllowlist: nullStrings(k.IpAllowlist),
		AllowOwnerQuery: k.AllowOwnerQuery, UsedAllMicro: k.UsedAllMicro, UsedBalanceMicro: k.UsedBalanceMicro,
		UsageResetAt: nullable.NewNullNullable[time.Time]()}
	if k.UsageResetAt.Year() > 1970 {
		out.UsageResetAt = nullable.NewNullableWithValue(k.UsageResetAt.UTC())
	}
	return out
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

func (s *Server) anyKey(ctx context.Context, id uuid.UUID) (sqlc.ApiKey, error) {
	k, err := s.Store.Q.GetAPIKey(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return k, notFound("key")
	}
	return k, err
}

// keyFields is the editable part of a key, as create and update send it.
type keyFields struct {
	Name            *string
	Tiers           *[]Tier
	SubscriptionID  nullable.Nullable[openapi_types.UUID]
	MaxConcurrency  nullable.Nullable[int]
	Rpm             nullable.Nullable[int]
	Enabled         *bool
	Note            *string
	QuotaLimit      nullable.Nullable[int64]
	QuotaScope      *QuotaScope
	Limit5h         nullable.Nullable[int64]
	Limit1d         nullable.Nullable[int64]
	Limit7d         nullable.Nullable[int64]
	ExpiresAt       nullable.Nullable[time.Time]
	ModelAllowlist  nullable.Nullable[[]string]
	IPAllowlist     nullable.Nullable[[]string]
	AllowOwnerQuery *bool
}

func fieldsOfCreate(b ApiKeyCreate) keyFields {
	return keyFields{Name: b.Name, Tiers: b.Tiers, SubscriptionID: b.SubscriptionId, MaxConcurrency: b.MaxConcurrency, Rpm: b.Rpm,
		Enabled: b.Enabled, Note: b.Note, QuotaLimit: b.QuotaLimitMicro, QuotaScope: b.QuotaScope, Limit5h: b.Limit5hMicro,
		Limit1d: b.Limit1dMicro, Limit7d: b.Limit7dMicro, ExpiresAt: b.ExpiresAt, ModelAllowlist: b.ModelAllowlist,
		IPAllowlist: b.IpAllowlist, AllowOwnerQuery: b.AllowOwnerQuery}
}

func fieldsOfUpdate(b ApiKeyUpdate) keyFields {
	return keyFields{Name: b.Name, Tiers: b.Tiers, SubscriptionID: b.SubscriptionId, MaxConcurrency: b.MaxConcurrency, Rpm: b.Rpm,
		Enabled: b.Enabled, Note: b.Note, QuotaLimit: b.QuotaLimitMicro, QuotaScope: b.QuotaScope, Limit5h: b.Limit5hMicro,
		Limit1d: b.Limit1dMicro, Limit7d: b.Limit7dMicro, ExpiresAt: b.ExpiresAt, ModelAllowlist: b.ModelAllowlist,
		IPAllowlist: b.IpAllowlist, AllowOwnerQuery: b.AllowOwnerQuery}
}

func optInt64(n nullable.Nullable[int64], cur *int64) (*int64, error) {
	if !n.IsSpecified() {
		return cur, nil
	}
	if n.IsNull() {
		return nil, nil
	}
	v := n.MustGet()
	if v < 0 {
		return nil, badRequest("limits must be >= 0")
	}
	return &v, nil
}

func optTime(n nullable.Nullable[time.Time], cur *time.Time) *time.Time {
	if !n.IsSpecified() {
		return cur
	}
	if n.IsNull() {
		return nil
	}
	v := n.MustGet().UTC()
	return &v
}

func normalizeModels(in []string) ([]string, error) {
	var out []string
	for _, m := range in {
		n := billing.NormalizeModel(m)
		if n == "" {
			continue
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) > maxAllowlist {
		return nil, badRequest(fmt.Sprintf("at most %d models", maxAllowlist))
	}
	return out, nil
}

// applyKeyFields merges the request into the current key (cur is the zero
// key on create) and validates the result.
func (s *Server) applyKeyFields(ctx context.Context, user uuid.UUID, cur sqlc.ApiKey, f keyFields, creating bool) (sqlc.UpdateAPIKeyParams, error) {
	p := updateParamsOf(cur)
	if f.Name != nil {
		p.Name = *f.Name
	}
	if f.Note != nil {
		p.Note = *f.Note
	}
	if f.Enabled != nil {
		p.Enabled = *f.Enabled
	}
	if f.SubscriptionID.IsSpecified() {
		if f.SubscriptionID.IsNull() {
			p.SubscriptionID = nil
		} else {
			v := f.SubscriptionID.MustGet()
			p.SubscriptionID = &v
		}
	}
	switch {
	case f.Tiers != nil:
		t, err := normalizeTiers(*f.Tiers)
		if err != nil {
			return p, err
		}
		p.Tiers = t
	case creating:
		p.Tiers = []string{router.TierStandard}
		if p.SubscriptionID != nil {
			p.Tiers = []string{router.TierPlan, router.TierStandard}
		}
	}
	if err := s.checkKeySubscription(ctx, user, p.Tiers, p.SubscriptionID); err != nil {
		return p, err
	}
	var err error
	if p.MaxConcurrency, err = optInt32(f.MaxConcurrency, p.MaxConcurrency); err != nil {
		return p, err
	}
	if p.Rpm, err = optInt32(f.Rpm, p.Rpm); err != nil {
		return p, err
	}
	if p.QuotaLimitMicro, err = optInt64(f.QuotaLimit, p.QuotaLimitMicro); err != nil {
		return p, err
	}
	if p.Limit5hMicro, err = optInt64(f.Limit5h, p.Limit5hMicro); err != nil {
		return p, err
	}
	if p.Limit1dMicro, err = optInt64(f.Limit1d, p.Limit1dMicro); err != nil {
		return p, err
	}
	if p.Limit7dMicro, err = optInt64(f.Limit7d, p.Limit7dMicro); err != nil {
		return p, err
	}
	if f.QuotaScope != nil {
		switch *f.QuotaScope {
		case QuotaScopeAll, QuotaScopeBalance:
			p.QuotaScope = string(*f.QuotaScope)
		default:
			return p, badRequest("quota_scope must be all or balance")
		}
	}
	p.ExpiresAt = optTime(f.ExpiresAt, p.ExpiresAt)
	if f.ModelAllowlist.IsSpecified() {
		p.ModelAllowlist = nil
		if !f.ModelAllowlist.IsNull() {
			if p.ModelAllowlist, err = normalizeModels(f.ModelAllowlist.MustGet()); err != nil {
				return p, err
			}
		}
	}
	if f.IPAllowlist.IsSpecified() {
		p.IpAllowlist = nil
		if !f.IPAllowlist.IsNull() {
			ips, err := keymeter.NormalizeIPs(f.IPAllowlist.MustGet())
			if err != nil {
				return p, badRequest(err.Error())
			}
			if len(ips) > maxAllowlist {
				return p, badRequest(fmt.Sprintf("at most %d IP entries", maxAllowlist))
			}
			p.IpAllowlist = ips
		}
	}
	if f.AllowOwnerQuery != nil {
		p.AllowOwnerQuery = *f.AllowOwnerQuery
	}
	if len(p.Name) > 100 || len(p.Note) > 500 {
		return p, badRequest("name or note too long")
	}
	return p, nil
}

func updateParamsOf(k sqlc.ApiKey) sqlc.UpdateAPIKeyParams {
	scope := k.QuotaScope
	if scope == "" {
		scope = "all"
	}
	return sqlc.UpdateAPIKeyParams{ID: k.ID, Name: k.Name, Tiers: k.Tiers, SubscriptionID: k.SubscriptionID,
		MaxConcurrency: k.MaxConcurrency, Rpm: k.Rpm, Enabled: k.Enabled, Note: k.Note, QuotaLimitMicro: k.QuotaLimitMicro,
		QuotaScope: scope, Limit5hMicro: k.Limit5hMicro, Limit1dMicro: k.Limit1dMicro, Limit7dMicro: k.Limit7dMicro,
		ExpiresAt: k.ExpiresAt, ModelAllowlist: k.ModelAllowlist, IpAllowlist: k.IpAllowlist, AllowOwnerQuery: k.AllowOwnerQuery}
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
	user := currentUser(ctx).ID
	p, err := s.applyKeyFields(ctx, user, sqlc.ApiKey{Enabled: true, QuotaScope: "all"}, fieldsOfCreate(*req.Body), true)
	if err != nil {
		return nil, err
	}
	raw := secret.NewAPIKey()
	id, _ := uuid.NewV7()
	var row sqlc.ApiKey
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		row, err = tx.Q.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{ID: id, UserID: user, Name: p.Name,
			KeyHash: secret.KeyHash(raw), KeyEnc: s.Box.Seal(raw), KeyPrefix: secret.KeyPrefix(raw), Tiers: p.Tiers,
			SubscriptionID: p.SubscriptionID, MaxConcurrency: p.MaxConcurrency, Rpm: p.Rpm, Enabled: p.Enabled, Note: p.Note,
			QuotaLimitMicro: p.QuotaLimitMicro, QuotaScope: p.QuotaScope, Limit5hMicro: p.Limit5hMicro, Limit1dMicro: p.Limit1dMicro,
			Limit7dMicro: p.Limit7dMicro, ExpiresAt: p.ExpiresAt, ModelAllowlist: p.ModelAllowlist, IpAllowlist: p.IpAllowlist,
			AllowOwnerQuery: p.AllowOwnerQuery})
		if err != nil {
			return err
		}
		return tx.InvalidateHashes(ctx, [][]byte{row.KeyHash})
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	// ApiKeyWithSecret is generated flattened: copy the fields through JSON.
	var out ApiKeyWithSecret
	b, _ := json.Marshal(keyOf(row))
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	out.Key = raw
	return CreateKey201JSONResponse(out), nil
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

func (s *Server) updateKey(ctx context.Context, k sqlc.ApiKey, f keyFields) (sqlc.ApiKey, error) {
	p, err := s.applyKeyFields(ctx, k.UserID, k, f, false)
	if err != nil {
		return k, err
	}
	var row sqlc.ApiKey
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		row, err = tx.Q.UpdateAPIKey(ctx, p)
		if err != nil {
			return err
		}
		return tx.InvalidateHashes(ctx, [][]byte{k.KeyHash})
	})
	return row, mapDomainErr(err)
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
	row, err := s.updateKey(ctx, k, fieldsOfUpdate(*req.Body))
	if err != nil {
		return nil, err
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

// resetKeyUsage zeroes the key's quota usage; the new (0, 0, version) reaches
// Redis through the outbox. Windows are not touched.
func (s *Server) resetKeyUsage(ctx context.Context, k sqlc.ApiKey) (sqlc.ApiKey, error) {
	var row sqlc.ApiKey
	err := s.Store.InTx(ctx, func(tx *store.Tx) error {
		var err error
		row, err = tx.Q.ResetAPIKeyUsage(ctx, k.ID)
		if err != nil {
			return err
		}
		return tx.Enqueue(ctx, store.OutboxKeySync, store.KeySync{Key: row.ID, All: 0, Balance: 0, Version: row.UsageVersion})
	})
	return row, mapDomainErr(err)
}

// ResetKeyUsage implements POST /keys/{keyId}/reset-usage.
func (s *Server) ResetKeyUsage(ctx context.Context, req ResetKeyUsageRequestObject) (ResetKeyUsageResponseObject, error) {
	k, err := s.ownKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	row, err := s.resetKeyUsage(ctx, k)
	if err != nil {
		return nil, err
	}
	return ResetKeyUsage200JSONResponse(keyOf(row)), nil
}

// KeyLimits builds the real-time limit status of a key.
func (s *Server) KeyLimits(ctx context.Context, k sqlc.ApiKey, now time.Time) (KeyLimitStatus, error) {
	u, err := s.Meter.Usage(ctx, k.ID, now)
	if err != nil {
		return KeyLimitStatus{}, err
	}
	scope := "a"
	if k.QuotaScope == "balance" {
		scope = "b"
	}
	out := KeyLimitStatus{QuotaScope: QuotaScope(k.QuotaScope), QuotaLimitMicro: nullInt64(k.QuotaLimitMicro),
		QuotaUsedMicro: u.Used(scope), QuotaHeldMicro: u.Held, QuotaRemainingMicro: nullable.NewNullNullable[int64](),
		UsedAllMicro: u.UsedAll, UsedBalanceMicro: u.UsedBalance, UsageResetAt: keyOf(k).UsageResetAt, Windows: []KeyWindowStatus{}}
	if out.QuotaScope == "" {
		out.QuotaScope = QuotaScopeAll
	}
	if k.QuotaLimitMicro != nil {
		out.QuotaRemainingMicro = nullable.NewNullableWithValue(*k.QuotaLimitMicro - u.Used(scope) - u.Held)
	}
	limits := []*int64{k.Limit5hMicro, k.Limit1dMicro, k.Limit7dMicro}
	for i, ws := range rstate.WindowSpans {
		held := u.Held
		w := KeyWindowStatus{Window: KeyWindowStatusWindow(ws.Name), UsedMicro: u.Window(scope, i), HeldMicro: &held,
			LimitMicro: nullInt64(limits[i]), RemainingMicro: nullable.NewNullNullable[int64](), RecoversAt: nullable.NewNullNullable[time.Time]()}
		if l := limits[i]; l != nil {
			rem := *l - w.UsedMicro - u.Held
			w.RemainingMicro = nullable.NewNullableWithValue(rem)
			if rem <= 0 {
				if t := s.Meter.Recovery(ctx, k.ID, scope, i, *l, now); t != nil {
					w.RecoversAt = nullable.NewNullableWithValue(*t)
				}
			}
		}
		out.Windows = append(out.Windows, w)
	}
	out.Exhausted = exhaustionOf(out, scope)
	return out, nil
}

// exhaustionOf reports the first exhausted limit (quota before windows) in
// the words of the relay's rejection; nil when nothing is used up.
func exhaustionOf(st KeyLimitStatus, scope string) *KeyExhaustion {
	var rej *keymeter.Rejection
	if r, err := st.QuotaRemainingMicro.Get(); err == nil && r <= 0 {
		rej = keymeter.QuotaRejection(scope)
	} else {
		for i, w := range st.Windows {
			if r, err := w.RemainingMicro.Get(); err == nil && r <= 0 {
				var rec *time.Time
				if t, err := w.RecoversAt.Get(); err == nil {
					rec = &t
				}
				rej = keymeter.WindowRejection(scope, i, rec)
				break
			}
		}
	}
	if rej == nil {
		return nil
	}
	out := &KeyExhaustion{Code: KeyExhaustionCode(rej.Code), Message: rej.Message, PlanAvailable: rej.TierOnly(),
		RecoversAt: nullable.NewNullNullable[time.Time]()}
	if rej.Window != "" {
		win := KeyExhaustionWindow(rej.Window)
		out.Window = &win
	}
	if rej.RecoversAt != nil {
		out.RecoversAt = nullable.NewNullableWithValue(*rej.RecoversAt)
	}
	return out
}

// GetKeyLimits implements GET /keys/{keyId}/limits.
func (s *Server) GetKeyLimits(ctx context.Context, req GetKeyLimitsRequestObject) (GetKeyLimitsResponseObject, error) {
	k, err := s.ownKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	out, err := s.KeyLimits(ctx, k, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return GetKeyLimits200JSONResponse(out), nil
}

// GetKeyDailyUsage implements GET /keys/{keyId}/usage/daily.
func (s *Server) GetKeyDailyUsage(ctx context.Context, req GetKeyDailyUsageRequestObject) (GetKeyDailyUsageResponseObject, error) {
	k, err := s.ownKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	days := deref(req.Params.Days, 30)
	if days < 1 || days > 90 {
		return nil, badRequest("days must be between 1 and 90")
	}
	loc := s.location()
	now := time.Now().In(loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	rows, err := s.Store.Q.KeyDailyUsage(ctx, sqlc.KeyDailyUsageParams{Tz: loc.String(), KeyID: k.ID, FromAt: from.UTC()})
	if err != nil {
		return nil, err
	}
	items := make([]KeyDailyRow, len(rows))
	for i, r := range rows {
		items[i] = KeyDailyRow{Day: openapi_types.Date{Time: r.Day}, Tier: r.Tier, Requests: r.Requests, Errors: r.Errors,
			InputTokens: r.InputTokens, CacheReadTokens: r.CacheReadTokens, CacheWriteTokens: r.CacheWriteTokens,
			OutputTokens: r.OutputTokens, ReasoningTokens: r.ReasoningTokens, BilledMicro: r.BilledMicro}
	}
	return GetKeyDailyUsage200JSONResponse{KeyId: k.ID, Days: days, Timezone: loc.String(), Items: items}, nil
}

// GetMyKeyUsage implements GET /usage/keys.
func (s *Server) GetMyKeyUsage(ctx context.Context, req GetMyKeyUsageRequestObject) (GetMyKeyUsageResponseObject, error) {
	from, to := req.Params.From.UTC(), req.Params.To.UTC()
	if !to.After(from) {
		return nil, badRequest("to must be after from")
	}
	if to.Sub(from) > 400*24*time.Hour {
		return nil, badRequest("range too large (max 400 days)")
	}
	user := currentUser(ctx).ID
	loc := s.location()
	totals, err := s.Store.Q.UserKeyTotals(ctx, sqlc.UserKeyTotalsParams{UserID: user, FromAt: from, ToAt: to})
	if err != nil {
		return nil, err
	}
	keyRows, err := s.Store.Q.ListAllAPIKeysByUser(ctx, user)
	if err != nil {
		return nil, err
	}
	names := map[uuid.UUID]sqlc.ListAllAPIKeysByUserRow{}
	for _, k := range keyRows {
		names[k.ID] = k
	}
	out := GetMyKeyUsage200JSONResponse{From: from, To: to, Timezone: loc.String(), Items: []KeyUsageTotal{}, Top: []KeySeries{}}
	var top []uuid.UUID
	for _, t := range totals {
		k := names[t.KeyID]
		out.Items = append(out.Items, KeyUsageTotal{KeyId: t.KeyID, Name: k.Name, Prefix: k.KeyPrefix, Deleted: k.DeletedAt != nil,
			Requests: t.Requests, Errors: t.Errors, InputTokens: t.InputTokens, CacheReadTokens: t.CacheReadTokens,
			CacheWriteTokens: t.CacheWriteTokens, OutputTokens: t.OutputTokens, ReasoningTokens: t.ReasoningTokens, BilledMicro: t.BilledMicro})
		if len(top) < 10 {
			top = append(top, t.KeyID)
		}
	}
	if len(top) > 0 {
		daily, err := s.Store.Q.UserKeyDaily(ctx, sqlc.UserKeyDailyParams{Tz: loc.String(), UserID: user, KeyIds: top, FromAt: from, ToAt: to})
		if err != nil {
			return nil, err
		}
		series := map[uuid.UUID]*KeySeries{}
		for _, id := range top {
			series[id] = &KeySeries{KeyId: id, Points: []KeyDailyPoint{}}
		}
		for _, d := range daily {
			if ks := series[d.KeyID]; ks != nil {
				ks.Points = append(ks.Points, KeyDailyPoint{Day: openapi_types.Date{Time: d.Day}, Requests: d.Requests, BilledMicro: d.BilledMicro})
			}
		}
		for _, id := range top {
			ks := series[id]
			sort.Slice(ks.Points, func(i, j int) bool { return ks.Points[i].Day.Before(ks.Points[j].Day.Time) })
			out.Top = append(out.Top, *ks)
		}
	}
	return out, nil
}

// ---- admin key management ------------------------------------------------------------

// AdminUpdateKey implements PATCH /admin/keys/{keyId}.
func (s *Server) AdminUpdateKey(ctx context.Context, req AdminUpdateKeyRequestObject) (AdminUpdateKeyResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	k, err := s.anyKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	row, err := s.updateKey(ctx, k, fieldsOfUpdate(*req.Body))
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "key.update", "api_key", k.ID.String(), keyAudit(k), keyAudit(row))
	return AdminUpdateKey200JSONResponse(keyOf(row)), nil
}

// AdminResetKeyUsage implements POST /admin/keys/{keyId}/reset-usage.
func (s *Server) AdminResetKeyUsage(ctx context.Context, req AdminResetKeyUsageRequestObject) (AdminResetKeyUsageResponseObject, error) {
	k, err := s.anyKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	row, err := s.resetKeyUsage(ctx, k)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "key.reset_usage", "api_key", k.ID.String(),
		map[string]any{"user_id": k.UserID, "used_all_micro": k.UsedAllMicro, "used_balance_micro": k.UsedBalanceMicro},
		map[string]any{"user_id": k.UserID, "used_all_micro": 0, "used_balance_micro": 0})
	return AdminResetKeyUsage200JSONResponse(keyOf(row)), nil
}

// AdminGetKeyLimits implements GET /admin/keys/{keyId}/limits.
func (s *Server) AdminGetKeyLimits(ctx context.Context, req AdminGetKeyLimitsRequestObject) (AdminGetKeyLimitsResponseObject, error) {
	k, err := s.anyKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	out, err := s.KeyLimits(ctx, k, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return AdminGetKeyLimits200JSONResponse(out), nil
}

// keyAudit is the audited form of a key (never the secret).
func keyAudit(k sqlc.ApiKey) map[string]any {
	return map[string]any{"user_id": k.UserID, "name": k.Name, "tiers": k.Tiers, "enabled": k.Enabled,
		"max_concurrency": k.MaxConcurrency, "rpm": k.Rpm, "quota_limit_micro": k.QuotaLimitMicro, "quota_scope": k.QuotaScope,
		"limit_5h_micro": k.Limit5hMicro, "limit_1d_micro": k.Limit1dMicro, "limit_7d_micro": k.Limit7dMicro,
		"expires_at": k.ExpiresAt, "model_allowlist": k.ModelAllowlist, "ip_allowlist": k.IpAllowlist,
		"allow_owner_query": k.AllowOwnerQuery}
}
