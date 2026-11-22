package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
)

// Redeem codes: an optional prefix plus four groups of four Crockford
// Base32 characters (80 random bits), e.g. OXA-7K2M-Q9TD-4HXR-PN3C. Only the
// 16 random characters identify a code: their sha256 is the lookup key and
// the sealed original is kept for the CSV export.

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Redemption limits per user: attempts per minute, and a lock after
// consecutive failures.
const (
	redeemPerMinute    = 10
	redeemMaxFailures  = 5
	redeemLockDuration = 15 * time.Minute
	redeemMaxBatch     = 1000
)

var redeemPrefixRe = regexp.MustCompile(`^[A-Z0-9]{1,8}$`)

// NewRedeemBody returns 16 random Crockford Base32 characters.
func NewRedeemBody() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	out := make([]byte, 16)
	for i, v := range b {
		out[i] = crockford[int(v)%32] // 256 is a multiple of 32: uniform
	}
	return string(out)
}

// FormatRedeemCode renders prefix + body as PREFIX-XXXX-XXXX-XXXX-XXXX.
func FormatRedeemCode(prefix, body string) string {
	groups := []string{body[0:4], body[4:8], body[8:12], body[12:16]}
	if prefix != "" {
		groups = append([]string{prefix}, groups...)
	}
	return strings.Join(groups, "-")
}

func redeemHint(prefix, body string) string {
	h := body[0:4] + "-…-" + body[12:16]
	if prefix != "" {
		h = prefix + "-" + h
	}
	return h
}

// NormalizeRedeemCode extracts the 16 random characters of a typed code:
// case, spaces and dashes do not matter, and the Crockford aliases O→0,
// I/L→1 are applied. ok is false when the input cannot be a code.
func NormalizeRedeemCode(in string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(in) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) < 16 {
		return "", false
	}
	body := []byte(s[len(s)-16:])
	for i, c := range body {
		switch c {
		case 'O':
			body[i] = '0'
		case 'I', 'L':
			body[i] = '1'
		}
		if !strings.ContainsRune(crockford, rune(body[i])) {
			return "", false
		}
	}
	return string(body), true
}

func redeemHash(body string) []byte {
	h := sha256.Sum256([]byte("oxalate-bridge:redeem:" + body))
	return h[:]
}

func redeemErr(code, msg string) error { return apiErr(http.StatusBadRequest, code, msg) }

// redeemLocked reports an active lock; recordRedeem counts the attempt.
func (s *Server) redeemLocked(ctx context.Context, user uuid.UUID) bool {
	n, err := s.St.R.Exists(ctx, s.St.Key("rdl", user.String())).Result()
	return err == nil && n > 0
}

func (s *Server) redeemFailed(ctx context.Context, user uuid.UUID) {
	fk := s.St.Key("rdf", user.String())
	n, err := s.St.R.Incr(ctx, fk).Result()
	if err != nil {
		return
	}
	s.St.R.Expire(ctx, fk, redeemLockDuration)
	if n >= redeemMaxFailures {
		s.St.R.Set(ctx, s.St.Key("rdl", user.String()), "1", redeemLockDuration)
		s.St.R.Del(ctx, fk)
	}
}

// RedeemCode implements POST /balance/redeem.
func (s *Server) RedeemCode(ctx context.Context, req RedeemCodeRequestObject) (RedeemCodeResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	user := currentUser(ctx).ID
	if s.redeemLocked(ctx, user) {
		return nil, apiErr(http.StatusTooManyRequests, "redeem_locked", "too many failed attempts; redemption is locked for a while")
	}
	if s.rateHit(ctx, "rd", user.String(), redeemPerMinute) {
		return nil, apiErr(http.StatusTooManyRequests, "rate_limited", "too many attempts, try again in a minute")
	}
	res, err := s.redeem(ctx, user, req.Body.Code)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && strings.HasPrefix(ae.Code, "redeem_code_") {
			s.redeemFailed(ctx, user)
		}
		return nil, err
	}
	s.St.R.Del(ctx, s.St.Key("rdf", user.String()))
	return RedeemCode200JSONResponse(res), nil
}

// redeem moves a code to used and credits the balance in one transaction:
// the conditional update lets exactly one transaction win, and the ledger
// entry is idempotent on the code id.
func (s *Server) redeem(ctx context.Context, user uuid.UUID, typed string) (RedeemResult, error) {
	body, ok := NormalizeRedeemCode(typed)
	if !ok {
		return RedeemResult{}, redeemErr("redeem_code_invalid", "invalid code")
	}
	var out RedeemResult
	err := s.Store.InTx(ctx, func(tx *store.Tx) error {
		code, err := tx.Q.GetRedeemCodeByHash(ctx, redeemHash(body))
		if errors.Is(err, pgx.ErrNoRows) {
			return redeemErr("redeem_code_invalid", "invalid code")
		}
		if err != nil {
			return err
		}
		switch {
		case code.Status == "used":
			return redeemErr("redeem_code_used", "code already used")
		case code.Status == "void":
			return redeemErr("redeem_code_void", "code has been voided")
		case code.ExpiresAt != nil && !time.Now().Before(*code.ExpiresAt):
			return redeemErr("redeem_code_expired", "code has expired")
		}
		used, err := tx.Q.UseRedeemCode(ctx, sqlc.UseRedeemCodeParams{ID: code.ID, UsedBy: &user})
		if errors.Is(err, pgx.ErrNoRows) {
			return redeemErr("redeem_code_used", "code already used")
		}
		if err != nil {
			return err
		}
		entry, inserted, err := tx.AppendLedger(ctx, store.LedgerParams{UserID: user, Type: "redeem", Amount: used.AmountMicro,
			RefType: "redeem_code", RefID: code.ID.String(), IdempotencyKey: "redeem:" + code.ID.String(),
			Note: code.CodeHint, SyncRedis: true})
		if err != nil {
			return err
		}
		if !inserted {
			return redeemErr("redeem_code_used", "code already used")
		}
		out = RedeemResult{AmountMicro: used.AmountMicro, BalanceMicro: entry.BalanceAfterMicro}
		return nil
	})
	return out, err
}

// ---- admin --------------------------------------------------------------------------

func batchOf(b sqlc.RedeemBatch, unused, used, voided int64) RedeemBatch {
	return RedeemBatch{Id: b.ID, Prefix: b.Prefix, AmountMicro: b.AmountMicro, Count: int(b.Count), ExpiresAt: nullTime(b.ExpiresAt),
		Note: b.Note, CreatedBy: nullUUID(b.CreatedBy), CreatedAt: b.CreatedAt, Unused: unused, Used: used, Voided: voided}
}

// AdminCreateRedeemBatch implements POST /admin/redeem-batches.
func (s *Server) AdminCreateRedeemBatch(ctx context.Context, req AdminCreateRedeemBatchRequestObject) (AdminCreateRedeemBatchResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body required")
	}
	b := req.Body
	if b.AmountMicro <= 0 {
		return nil, badRequest("amount_micro must be > 0")
	}
	if b.Count < 1 || b.Count > redeemMaxBatch {
		return nil, badRequest(fmt.Sprintf("count must be between 1 and %d", redeemMaxBatch))
	}
	prefix := strings.ToUpper(strings.TrimSpace(deref(b.Prefix, "")))
	if prefix != "" && !redeemPrefixRe.MatchString(prefix) {
		return nil, badRequest("prefix must be 1-8 letters or digits")
	}
	var expires *time.Time
	if b.ExpiresAt.IsSpecified() && !b.ExpiresAt.IsNull() {
		v := b.ExpiresAt.MustGet().UTC()
		if !v.After(time.Now()) {
			return nil, badRequest("expires_at must be in the future")
		}
		expires = &v
	}
	note := deref(b.Note, "")
	if len(note) > 500 {
		return nil, badRequest("note too long")
	}
	op := currentUser(ctx).ID
	id, _ := uuid.NewV7()
	var batch sqlc.RedeemBatch
	err := s.Store.InTx(ctx, func(tx *store.Tx) error {
		var err error
		batch, err = tx.Q.CreateRedeemBatch(ctx, sqlc.CreateRedeemBatchParams{ID: id, Prefix: prefix, AmountMicro: b.AmountMicro,
			Count: int32(b.Count), ExpiresAt: expires, Note: note, CreatedBy: &op})
		if err != nil {
			return err
		}
		for i := 0; i < b.Count; i++ {
			body := NewRedeemBody()
			cid, _ := uuid.NewV7()
			if err := tx.Q.InsertRedeemCode(ctx, sqlc.InsertRedeemCodeParams{ID: cid, BatchID: id, CodeHash: redeemHash(body),
				CodeEnc: s.Box.Seal(FormatRedeemCode(prefix, body)), CodeHint: redeemHint(prefix, body)}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	s.audit(ctx, "redeem.create_batch", "redeem_batch", id.String(), nil,
		map[string]any{"amount_micro": b.AmountMicro, "count": b.Count, "prefix": prefix, "expires_at": expires, "note": note})
	return AdminCreateRedeemBatch201JSONResponse(batchOf(batch, int64(b.Count), 0, 0)), nil
}

// AdminListRedeemBatches implements GET /admin/redeem-batches.
func (s *Server) AdminListRedeemBatches(ctx context.Context, req AdminListRedeemBatchesRequestObject) (AdminListRedeemBatchesResponseObject, error) {
	at, cid, err := decodeCursor(req.Params.Cursor)
	if err != nil {
		return nil, err
	}
	lim := limitOf(req.Params.Limit)
	rows, err := s.Store.Q.ListRedeemBatches(ctx, sqlc.ListRedeemBatchesParams{Limit: lim, CursorAt: at, CursorID: cid})
	if err != nil {
		return nil, err
	}
	out := AdminListRedeemBatches200JSONResponse{Items: make([]RedeemBatch, len(rows)), NextCursor: nullable.NewNullNullable[string]()}
	for i, r := range rows {
		out.Items[i] = batchOf(sqlc.RedeemBatch{ID: r.ID, Prefix: r.Prefix, AmountMicro: r.AmountMicro, Count: r.Count, ExpiresAt: r.ExpiresAt,
			Note: r.Note, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt}, r.Unused, r.Used, r.Voided)
	}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		out.NextCursor = nextCursor(len(rows), lim, last.CreatedAt, last.ID)
	}
	return out, nil
}

func codeOf(c sqlc.RedeemCode, amount int64, expires *time.Time, now time.Time) RedeemCode {
	expired := expires != nil && !now.Before(*expires)
	return RedeemCode{Id: c.ID, BatchId: c.BatchID, Hint: c.CodeHint, Status: RedeemCodeStatus(c.Status), AmountMicro: amount,
		ExpiresAt: nullTime(expires), Expired: &expired, UsedBy: nullUUID(c.UsedBy), UsedAt: nullTime(c.UsedAt),
		VoidedAt: nullTime(c.VoidedAt), CreatedAt: c.CreatedAt}
}

// AdminListRedeemCodes implements GET /admin/redeem-codes.
func (s *Server) AdminListRedeemCodes(ctx context.Context, req AdminListRedeemCodesRequestObject) (AdminListRedeemCodesResponseObject, error) {
	p := req.Params
	at, cid, err := decodeCursor(p.Cursor)
	if err != nil {
		return nil, err
	}
	lim := limitOf(p.Limit)
	var status *string
	if p.Status != nil {
		v := string(*p.Status)
		status = &v
	}
	rows, err := s.Store.Q.ListRedeemCodes(ctx, sqlc.ListRedeemCodesParams{Limit: lim, BatchID: p.BatchId, Status: status, CursorAt: at, CursorID: cid})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := AdminListRedeemCodes200JSONResponse{Items: make([]RedeemCode, len(rows)), NextCursor: nullable.NewNullNullable[string]()}
	for i, r := range rows {
		out.Items[i] = codeOf(sqlc.RedeemCode{ID: r.ID, BatchID: r.BatchID, CodeHint: r.CodeHint, Status: r.Status, UsedBy: r.UsedBy,
			UsedAt: r.UsedAt, VoidedAt: r.VoidedAt, CreatedAt: r.CreatedAt}, r.AmountMicro, r.ExpiresAt, now)
	}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		out.NextCursor = nextCursor(len(rows), lim, last.CreatedAt, last.ID)
	}
	return out, nil
}

// AdminVoidRedeemCode implements POST /admin/redeem-codes/{codeId}/void.
func (s *Server) AdminVoidRedeemCode(ctx context.Context, req AdminVoidRedeemCodeRequestObject) (AdminVoidRedeemCodeResponseObject, error) {
	cur, err := s.Store.Q.GetRedeemCode(ctx, req.CodeId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	row, err := s.Store.Q.VoidRedeemCode(ctx, req.CodeId)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalidState("only unused codes can be voided")
	}
	if err != nil {
		return nil, err
	}
	batch, err := s.Store.Q.GetRedeemBatch(ctx, row.BatchID)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "redeem.void", "redeem_code", row.ID.String(), map[string]any{"status": cur.Status, "hint": cur.CodeHint},
		map[string]any{"status": row.Status, "hint": row.CodeHint})
	return AdminVoidRedeemCode200JSONResponse(codeOf(row, batch.AmountMicro, batch.ExpiresAt, time.Now())), nil
}

// AdminExportRedeemBatch implements GET /admin/redeem-batches/{batchId}/export.
func (s *Server) AdminExportRedeemBatch(ctx context.Context, req AdminExportRedeemBatchRequestObject) (AdminExportRedeemBatchResponseObject, error) {
	batch, err := s.Store.Q.GetRedeemBatch(ctx, req.BatchId)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	codes, err := s.Store.Q.ListRedeemCodesByBatch(ctx, batch.ID)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"code", "amount_usd", "status", "used_by", "used_at", "expires_at"})
	ts := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	for _, c := range codes {
		raw, err := s.Box.Open(c.CodeEnc)
		if err != nil {
			return nil, err
		}
		usedBy := ""
		if c.UsedBy != nil {
			usedBy = c.UsedBy.String()
		}
		_ = w.Write([]string{raw, strconv.FormatFloat(billing.MicroToUSD(batch.AmountMicro), 'f', -1, 64), c.Status, usedBy, ts(c.UsedAt), ts(batch.ExpiresAt)})
	}
	w.Flush()
	s.audit(ctx, "redeem.export", "redeem_batch", batch.ID.String(), nil, map[string]any{"codes": len(codes)})
	return AdminExportRedeemBatch200TextcsvResponse{Body: bytes.NewReader(buf.Bytes()), ContentLength: int64(buf.Len())}, nil
}
