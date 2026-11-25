package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/safeguard"
)

// recentSafeguardItems is how many triggers / blocked requests the user
// panel lists.
const recentSafeguardItems = 20

// safeguardErr maps the safeguard service errors to panel errors.
func safeguardErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, safeguard.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		return notFound("resource")
	case errors.Is(err, safeguard.ErrNoResetCard):
		return apiErr(http.StatusConflict, "safeguard_no_reset_card", "没有可用的重置卡")
	case errors.Is(err, safeguard.ErrNothingToReset):
		return apiErr(http.StatusConflict, "safeguard_nothing_to_reset", "safeguard 次数已经是 0，不需要重置")
	case errors.Is(err, safeguard.ErrNotLocked):
		return apiErr(http.StatusConflict, "safeguard_not_locked", "当前没有被封禁，不需要申请解封")
	case errors.Is(err, safeguard.ErrAppealPending):
		return apiErr(http.StatusConflict, "safeguard_appeal_pending", "已有一条待处理的解封申请")
	case errors.Is(err, safeguard.ErrAppealDecided):
		return apiErr(http.StatusConflict, "safeguard_appeal_decided", "这条申请已经处理过了")
	}
	return err
}

func (s *Server) defaultAllowance() int { return s.Settings.Get().SafeguardDefaultAllowance }

// ---- user ---------------------------------------------------------------------

// GetMySafeguard implements GET /me/safeguard.
func (s *Server) GetMySafeguard(ctx context.Context, _ GetMySafeguardRequestObject) (GetMySafeguardResponseObject, error) {
	out, err := s.mySafeguard(ctx, currentUser(ctx).ID)
	if err != nil {
		return nil, err
	}
	return GetMySafeguard200JSONResponse(out), nil
}

// UseSafeguardResetCard implements POST /me/safeguard/reset-card.
func (s *Server) UseSafeguardResetCard(ctx context.Context, _ UseSafeguardResetCardRequestObject) (UseSafeguardResetCardResponseObject, error) {
	uid := currentUser(ctx).ID
	st, err := s.Safeguard.UseResetCard(ctx, uid)
	if err != nil {
		return nil, safeguardErr(err)
	}
	s.audit(ctx, "safeguard.reset_card", "user", uid.String(), nil, map[string]any{"reset_cards": st.SafeguardResetCards})
	out, err := s.mySafeguard(ctx, uid)
	if err != nil {
		return nil, err
	}
	return UseSafeguardResetCard200JSONResponse(out), nil
}

// SubmitSafeguardAppeal implements POST /me/safeguard/appeals.
func (s *Server) SubmitSafeguardAppeal(ctx context.Context, req SubmitSafeguardAppealRequestObject) (SubmitSafeguardAppealResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	note := strings.TrimSpace(req.Body.Note)
	if note == "" || utf8.RuneCountInString(note) > 2000 {
		return nil, badRequest("note must be 1..2000 characters")
	}
	me := currentUser(ctx)
	a, err := s.Safeguard.SubmitAppeal(ctx, me.ID, note)
	if err != nil {
		return nil, safeguardErr(err)
	}
	s.audit(ctx, "safeguard.appeal_submit", "safeguard_appeal", a.ID.String(), nil, map[string]any{"note": note})
	out, err := s.mySafeguard(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	if s.Notify != nil {
		if u, err := s.Store.Q.GetUserSafeguard(ctx, me.ID); err == nil {
			s.Notify.AppealSubmitted(context.WithoutCancel(ctx), a.ID, u.Username, note)
		}
	}
	return SubmitSafeguardAppeal200JSONResponse(out), nil
}

func (s *Server) mySafeguard(ctx context.Context, uid uuid.UUID) (MySafeguard, error) {
	u, err := s.Store.Q.GetUserSafeguard(ctx, uid)
	if err != nil {
		return MySafeguard{}, mapDomainErr(err)
	}
	def := s.defaultAllowance()
	allow := safeguard.Allowance(u.SafeguardAllowance, def)
	locked := safeguard.Locked(u.SafeguardUsed, u.SafeguardAllowance, def)
	out := MySafeguard{Allowance: allow, Used: int(u.SafeguardUsed), Remaining: max(allow-int(u.SafeguardUsed), 0),
		ResetCards: int(u.SafeguardResetCards), Locked: locked, LockedAt: nullable.NewNullNullable[time.Time](),
		Appeal: nullable.NewNullNullable[MySafeguardAppeal](), RecentEvents: []MySafeguardEvent{}, RecentBlocked: []MySafeguardBlocked{}}
	if locked {
		out.LockedAt = nullTime(u.SafeguardLockedAt)
	}
	if a, err := s.Store.Q.GetLatestSafeguardAppeal(ctx, uid); err == nil {
		out.Appeal = nullable.NewNullableWithValue(MySafeguardAppeal{Id: a.ID, Note: a.Note, Status: SafeguardAppealStatus(a.Status),
			RejectReason: a.RejectReason, CreatedAt: a.CreatedAt, DecidedAt: nullTime(a.DecidedAt)})
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if out.EventsCount, err = s.Store.Q.CountUserSafeguardEvents(ctx, &uid); err != nil {
		return out, err
	}
	evs, err := s.Store.Q.ListUserSafeguardEvents(ctx, sqlc.ListUserSafeguardEventsParams{UserID: &uid, Limit: recentSafeguardItems})
	if err != nil {
		return out, err
	}
	for _, e := range evs {
		out.RecentEvents = append(out.RecentEvents, MySafeguardEvent{CreatedAt: e.CreatedAt, Model: e.Model, Category: e.Category, Counted: e.Counted})
	}
	if out.BlockedCount, err = s.Store.Q.CountUserBlockedRequests(ctx, uid); err != nil {
		return out, err
	}
	bl, err := s.Store.Q.ListUserBlockedRequests(ctx, sqlc.ListUserBlockedRequestsParams{UserID: uid, Limit: recentSafeguardItems})
	if err != nil {
		return out, err
	}
	for _, b := range bl {
		out.RecentBlocked = append(out.RecentBlocked, MySafeguardBlocked{CreatedAt: b.CreatedAt, KeyName: b.KeyName, KeyPrefix: b.KeyPrefix})
	}
	return out, nil
}

// ---- admin: users --------------------------------------------------------------

// AdminGetUserSafeguard implements GET /admin/users/{userId}/safeguard.
func (s *Server) AdminGetUserSafeguard(ctx context.Context, req AdminGetUserSafeguardRequestObject) (AdminGetUserSafeguardResponseObject, error) {
	out, err := s.adminUserSafeguard(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	return AdminGetUserSafeguard200JSONResponse(out), nil
}

// AdminUpdateUserSafeguard implements PATCH /admin/users/{userId}/safeguard.
func (s *Server) AdminUpdateUserSafeguard(ctx context.Context, req AdminUpdateUserSafeguardRequestObject) (AdminUpdateUserSafeguardResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	b := req.Body
	var adj safeguard.Adjustment
	if b.AllowanceOverride.IsSpecified() {
		adj.SetAllowance = true
		if !b.AllowanceOverride.IsNull() {
			v := b.AllowanceOverride.MustGet()
			if v < 1 || v > 1000 {
				return nil, badRequest("allowance_override must be within 1..1000")
			}
			n := int32(v)
			adj.Allowance = &n
		}
	}
	if b.Used != nil {
		if *b.Used < 0 || *b.Used > 100000 {
			return nil, badRequest("used must be within 0..100000")
		}
		n := int32(*b.Used)
		adj.Used = &n
	}
	if b.ResetCards != nil {
		if *b.ResetCards < 0 || *b.ResetCards > 1000 {
			return nil, badRequest("reset_cards must be within 0..1000")
		}
		n := int32(*b.ResetCards)
		adj.ResetCards = &n
	}
	before, after, err := s.Safeguard.Adjust(ctx, req.UserId, adj)
	if err != nil {
		return nil, safeguardErr(err)
	}
	s.audit(ctx, "safeguard.user_update", "user", req.UserId.String(), stateAudit(before), stateAudit(after))
	out, err := s.adminUserSafeguard(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	return AdminUpdateUserSafeguard200JSONResponse(out), nil
}

func stateAudit(u safeguard.UserState) map[string]any {
	return map[string]any{"allowance_override": u.SafeguardAllowance, "used": u.SafeguardUsed, "reset_cards": u.SafeguardResetCards}
}

func (s *Server) adminUserSafeguard(ctx context.Context, uid uuid.UUID) (AdminUserSafeguard, error) {
	u, err := s.Store.Q.GetUserSafeguard(ctx, uid)
	if err != nil {
		return AdminUserSafeguard{}, mapDomainErr(err)
	}
	def := s.defaultAllowance()
	locked := safeguard.Locked(u.SafeguardUsed, u.SafeguardAllowance, def)
	out := AdminUserSafeguard{UserId: u.ID, Username: u.Username, Allowance: safeguard.Allowance(u.SafeguardAllowance, def),
		AllowanceOverride: nullInt32(u.SafeguardAllowance), DefaultAllowance: def, Used: int(u.SafeguardUsed),
		ResetCards: int(u.SafeguardResetCards), Locked: locked, LockedAt: nullable.NewNullNullable[time.Time]()}
	if locked {
		out.LockedAt = nullTime(u.SafeguardLockedAt)
	}
	if out.EventsCount, err = s.Store.Q.CountUserSafeguardEvents(ctx, &uid); err != nil {
		return out, err
	}
	if out.BlockedCount, err = s.Store.Q.CountUserBlockedRequests(ctx, uid); err != nil {
		return out, err
	}
	return out, nil
}

// ---- admin: events -------------------------------------------------------------

// AdminListSafeguardEvents implements GET /admin/safeguard-events.
func (s *Server) AdminListSafeguardEvents(ctx context.Context, req AdminListSafeguardEventsRequestObject) (AdminListSafeguardEventsResponseObject, error) {
	at, id, err := decodeCursor(req.Params.Cursor)
	if err != nil {
		return nil, err
	}
	lim := limitOf(req.Params.Limit)
	var hash *string
	if h := req.Params.PromptHash; h != nil && *h != "" {
		hash = h
	}
	rows, err := s.Store.Q.ListSafeguardEvents(ctx, sqlc.ListSafeguardEventsParams{Limit: lim, UserID: req.Params.UserId,
		PromptHash: hash, CursorAt: at, CursorID: id})
	if err != nil {
		return nil, err
	}
	items := make([]SafeguardEvent, len(rows))
	for i, r := range rows {
		items[i] = safeguardEventOf(sqlc.GetSafeguardEventRow(r))
	}
	page := SafeguardEventPage{Items: items, NextCursor: nullable.NewNullNullable[string]()}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = nextCursor(len(rows), lim, last.CreatedAt, last.ID)
	}
	return AdminListSafeguardEvents200JSONResponse(page), nil
}

func safeguardEventOf(r sqlc.GetSafeguardEventRow) SafeguardEvent {
	return SafeguardEvent{Id: r.ID, RequestId: r.RequestID, UserId: nullUUID(r.UserID), KeyId: nullUUID(r.KeyID),
		AccountId: nullUUID(r.AccountID), Model: r.Model, Category: r.Category, Reason: r.Reason, CreatedAt: r.CreatedAt,
		StatusCode: int(r.StatusCode), Stream: r.Stream, Counted: r.Counted, PromptHash: r.PromptHash, Prompt: r.Prompt,
		HasBody: r.HasBody, BodySize: r.BodySize, Username: r.Username, KeyName: r.KeyName, KeyPrefix: r.KeyPrefix,
		AccountName: r.AccountName}
}

// AdminGetSafeguardEvent implements GET /admin/safeguard-events/{eventId}.
func (s *Server) AdminGetSafeguardEvent(ctx context.Context, req AdminGetSafeguardEventRequestObject) (AdminGetSafeguardEventResponseObject, error) {
	r, err := s.Store.Q.GetSafeguardEvent(ctx, req.EventId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return AdminGetSafeguardEvent200JSONResponse(safeguardEventOf(r)), nil
}

// AdminGetSafeguardEventBody implements GET /admin/safeguard-events/{eventId}/body.
func (s *Server) AdminGetSafeguardEventBody(ctx context.Context, req AdminGetSafeguardEventBodyRequestObject) (AdminGetSafeguardEventBodyResponseObject, error) {
	offset, limit := int64(0), int64(64<<10)
	if p := req.Params.Offset; p != nil {
		offset = *p
	}
	if p := req.Params.Limit; p != nil {
		limit = *p
	}
	if offset < 0 || limit < 1024 || limit > 1<<20 {
		return nil, badRequest("offset must be >= 0 and limit within 1024..1048576")
	}
	row, err := s.Store.Q.GetSafeguardEventBody(ctx, req.EventId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if row.BodyGz == nil {
		return nil, notFound("body")
	}
	if offset > row.BodySize {
		return nil, badRequest("offset beyond the end of the body")
	}
	text, next, err := safeguard.Chunk(row.BodyGz, offset, limit)
	if err != nil {
		return nil, err
	}
	if offset == 0 {
		s.audit(ctx, "safeguard.body_view", "safeguard_event", req.EventId.String(), nil, nil)
	}
	out := SafeguardBodyChunk{Text: text, Offset: offset, TotalSize: row.BodySize, NextOffset: nullable.NewNullNullable[int64]()}
	if next >= 0 {
		out.NextOffset = nullable.NewNullableWithValue(next)
	}
	return AdminGetSafeguardEventBody200JSONResponse(out), nil
}

// AdminDownloadSafeguardEventBody implements GET /admin/safeguard-events/{eventId}/raw.
func (s *Server) AdminDownloadSafeguardEventBody(ctx context.Context, req AdminDownloadSafeguardEventBodyRequestObject) (AdminDownloadSafeguardEventBodyResponseObject, error) {
	row, err := s.Store.Q.GetSafeguardEventBody(ctx, req.EventId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if row.BodyGz == nil {
		return nil, notFound("body")
	}
	zr, err := safeguard.Gunzip(row.BodyGz)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "safeguard.body_download", "safeguard_event", req.EventId.String(), nil, nil)
	disp := fmt.Sprintf(`attachment; filename="safeguard-%s.json"`, req.EventId)
	return AdminDownloadSafeguardEventBody200ApplicationoctetStreamResponse{Body: zr, ContentLength: row.BodySize,
		Headers: AdminDownloadSafeguardEventBody200ResponseHeaders{ContentDisposition: &disp}}, nil
}

// ---- admin: blocklist ------------------------------------------------------------

func encodeHashCursor(at time.Time, hash string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + hash))
}

func decodeHashCursor(c *string) (*time.Time, *string, error) {
	if c == nil || *c == "" {
		return nil, nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(*c)
	if err != nil {
		return nil, nil, apiErr(http.StatusBadRequest, "bad_request", "invalid cursor")
	}
	ts, hash, ok := bytes.Cut(raw, []byte("|"))
	if !ok {
		return nil, nil, apiErr(http.StatusBadRequest, "bad_request", "invalid cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, string(ts))
	if err != nil {
		return nil, nil, apiErr(http.StatusBadRequest, "bad_request", "invalid cursor")
	}
	h := string(hash)
	return &at, &h, nil
}

func safeguardBlockOf(r sqlc.GetSafeguardBlockRow) SafeguardBlock {
	return SafeguardBlock{Hash: r.Hash, Prompt: r.Prompt, FirstEventId: nullUUID(&r.FirstEventID), FirstUserId: nullUUID(r.FirstUserID),
		FirstUsername: r.FirstUsername, CreatedAt: r.CreatedAt, Hits: r.Hits, LastHitAt: nullTime(r.LastHitAt),
		RevokedAt: nullTime(r.RevokedAt), RevokedByUsername: r.RevokedByUsername}
}

// AdminListSafeguardBlocks implements GET /admin/safeguard-blocks.
func (s *Server) AdminListSafeguardBlocks(ctx context.Context, req AdminListSafeguardBlocksRequestObject) (AdminListSafeguardBlocksResponseObject, error) {
	at, hash, err := decodeHashCursor(req.Params.Cursor)
	if err != nil {
		return nil, err
	}
	status := "active"
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	var search *string
	if q := req.Params.Q; q != nil && strings.TrimSpace(*q) != "" {
		v := strings.TrimSpace(*q)
		search = &v
	}
	lim := limitOf(req.Params.Limit)
	rows, err := s.Store.Q.ListSafeguardBlocks(ctx, sqlc.ListSafeguardBlocksParams{Limit: lim, Status: status, Search: search,
		CursorAt: at, CursorHash: hash})
	if err != nil {
		return nil, err
	}
	items := make([]SafeguardBlock, len(rows))
	for i, r := range rows {
		items[i] = safeguardBlockOf(sqlc.GetSafeguardBlockRow(r))
	}
	page := SafeguardBlockPage{Items: items, NextCursor: nullable.NewNullNullable[string]()}
	if len(rows) == int(lim) {
		last := rows[len(rows)-1]
		page.NextCursor = nullable.NewNullableWithValue(encodeHashCursor(last.CreatedAt, last.Hash))
	}
	return AdminListSafeguardBlocks200JSONResponse(page), nil
}

// AdminGetSafeguardBlock implements GET /admin/safeguard-blocks/{hash}.
func (s *Server) AdminGetSafeguardBlock(ctx context.Context, req AdminGetSafeguardBlockRequestObject) (AdminGetSafeguardBlockResponseObject, error) {
	r, err := s.Store.Q.GetSafeguardBlock(ctx, req.Hash)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return AdminGetSafeguardBlock200JSONResponse(safeguardBlockOf(r)), nil
}

// AdminRevokeSafeguardBlock implements POST /admin/safeguard-blocks/{hash}/revoke.
func (s *Server) AdminRevokeSafeguardBlock(ctx context.Context, req AdminRevokeSafeguardBlockRequestObject) (AdminRevokeSafeguardBlockResponseObject, error) {
	if err := s.Safeguard.Revoke(ctx, req.Hash, currentUser(ctx).ID); err != nil {
		if errors.Is(err, safeguard.ErrNotFound) {
			if _, gerr := s.Store.Q.GetSafeguardBlock(ctx, req.Hash); gerr == nil {
				return nil, invalidState("这条已经撤销了")
			}
		}
		return nil, safeguardErr(err)
	}
	r, err := s.Store.Q.GetSafeguardBlock(ctx, req.Hash)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	out := safeguardBlockOf(r)
	s.audit(ctx, "safeguard.block_revoke", "safeguard_block", req.Hash, nil, map[string]any{"hits": r.Hits})
	return AdminRevokeSafeguardBlock200JSONResponse(out), nil
}

// ---- admin: appeals --------------------------------------------------------------

func (s *Server) safeguardAppealOf(r sqlc.GetSafeguardAppealViewRow) SafeguardAppeal {
	return SafeguardAppeal{Id: r.ID, UserId: r.UserID, Username: r.Username, Note: r.Note, Status: SafeguardAppealStatus(r.Status),
		RejectReason: r.RejectReason, CreatedAt: r.CreatedAt, DecidedAt: nullTime(r.DecidedAt), DecidedByUsername: r.DecidedByUsername,
		Used: int(r.SafeguardUsed), Allowance: safeguard.Allowance(r.SafeguardAllowance, s.defaultAllowance()),
		ResetCards: int(r.SafeguardResetCards)}
}

// AdminListSafeguardAppeals implements GET /admin/safeguard-appeals.
func (s *Server) AdminListSafeguardAppeals(ctx context.Context, req AdminListSafeguardAppealsRequestObject) (AdminListSafeguardAppealsResponseObject, error) {
	at, id, err := decodeCursor(req.Params.Cursor)
	if err != nil {
		return nil, err
	}
	status := "all"
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	lim := limitOf(req.Params.Limit)
	rows, err := s.Store.Q.ListSafeguardAppeals(ctx, sqlc.ListSafeguardAppealsParams{Limit: lim, Status: status, UserID: req.Params.UserId,
		CursorAt: at, CursorID: id})
	if err != nil {
		return nil, err
	}
	pending, err := s.Store.Q.CountPendingSafeguardAppeals(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]SafeguardAppeal, len(rows))
	for i, r := range rows {
		items[i] = s.safeguardAppealOf(sqlc.GetSafeguardAppealViewRow(r))
	}
	page := SafeguardAppealPage{Items: items, Pending: pending, NextCursor: nullable.NewNullNullable[string]()}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = nextCursor(len(rows), lim, last.CreatedAt, last.ID)
	}
	return AdminListSafeguardAppeals200JSONResponse(page), nil
}

func (s *Server) decideAppeal(ctx context.Context, id uuid.UUID, approve bool, reason string) (SafeguardAppeal, error) {
	a, err := s.Safeguard.DecideAppeal(ctx, id, currentUser(ctx).ID, approve, reason)
	if err != nil {
		return SafeguardAppeal{}, safeguardErr(err)
	}
	action := "safeguard.appeal_reject"
	if approve {
		action = "safeguard.appeal_approve"
	}
	s.audit(ctx, action, "safeguard_appeal", a.ID.String(), map[string]any{"status": "pending"},
		map[string]any{"status": a.Status, "reject_reason": a.RejectReason, "user_id": a.UserID})
	if s.Notify != nil {
		s.Notify.AppealDecided(context.WithoutCancel(ctx), a.ID, a.UserID, approve, a.RejectReason)
	}
	v, err := s.Store.Q.GetSafeguardAppealView(ctx, a.ID)
	if err != nil {
		return SafeguardAppeal{}, err
	}
	return s.safeguardAppealOf(v), nil
}

// AdminApproveSafeguardAppeal implements POST /admin/safeguard-appeals/{appealId}/approve.
func (s *Server) AdminApproveSafeguardAppeal(ctx context.Context, req AdminApproveSafeguardAppealRequestObject) (AdminApproveSafeguardAppealResponseObject, error) {
	out, err := s.decideAppeal(ctx, req.AppealId, true, "")
	if err != nil {
		return nil, err
	}
	return AdminApproveSafeguardAppeal200JSONResponse(out), nil
}

// AdminRejectSafeguardAppeal implements POST /admin/safeguard-appeals/{appealId}/reject.
func (s *Server) AdminRejectSafeguardAppeal(ctx context.Context, req AdminRejectSafeguardAppealRequestObject) (AdminRejectSafeguardAppealResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	reason := strings.TrimSpace(req.Body.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > 1000 {
		return nil, badRequest("reason must be 1..1000 characters")
	}
	out, err := s.decideAppeal(ctx, req.AppealId, false, reason)
	if err != nil {
		return nil, err
	}
	return AdminRejectSafeguardAppeal200JSONResponse(out), nil
}
