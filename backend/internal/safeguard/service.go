package safeguard

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/settings"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
)

// Errors of the user and admin operations.
var (
	ErrNotFound       = errors.New("not found")
	ErrNoResetCard    = errors.New("no reset card left")
	ErrNothingToReset = errors.New("no safeguard trigger to reset")
	ErrNotLocked      = errors.New("the account is not locked")
	ErrAppealPending  = errors.New("an appeal is already pending")
	ErrAppealDecided  = errors.New("the appeal was already decided")
)

// Service records triggers and keeps the blocklist.
type Service struct {
	Store    *store.Store
	St       *rstate.State
	Settings *settings.Store
	Log      *slog.Logger
	Now      func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// BlocksKey is the Redis set of active blocklist hashes.
func (s *Service) BlocksKey() string { return s.St.Key("safeguard", "blocks") }

// Allowance is a user's effective allowance (own or the default).
func Allowance(override *int32, def int) int {
	if override != nil {
		return int(*override)
	}
	return def
}

// Locked reports whether a user has used up the allowance.
func Locked(used int32, override *int32, def int) bool {
	return int(used) >= Allowance(override, def)
}

// Trigger is one refused request.
type Trigger struct {
	RequestID  uuid.UUID
	UserID     uuid.UUID
	KeyID      uuid.UUID
	AccountID  *uuid.UUID
	Model      string
	Refusal    Refusal
	StatusCode int
	Stream     bool
	Body       []byte
	At         time.Time
}

// Recorded is the outcome of Record.
type Recorded struct {
	EventID uuid.UUID
	Hash    string
	// Counted is false when the prompt was already on the blocklist (a
	// concurrent request of the same prompt triggered first) or the request
	// was recorded before.
	Counted bool
	// Locked is true when this trigger used up the user's allowance.
	Locked bool
}

// Record stores a trigger: the event with the compressed request body, the
// prompt hash on the blocklist and one use of the user's allowance.
func (s *Service) Record(ctx context.Context, t Trigger) (Recorded, error) {
	set := s.Settings.Get()
	prompt := Prompt(t.Body)
	hash := Hash(prompt, set.SafeguardHashMinChars)
	gz, err := Gzip(t.Body)
	if err != nil {
		return Recorded{}, err
	}
	at := t.At
	if at.IsZero() {
		at = s.now()
	}
	out := Recorded{Hash: hash}
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		counted := true
		if hash != "" {
			// Serializes triggers of the same prompt: the second one sees the
			// block the first inserted and is not counted.
			if _, err := tx.Raw.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "safeguard:"+hash); err != nil {
				return err
			}
			b, err := tx.Q.GetSafeguardBlockForUpdate(ctx, hash)
			switch {
			case err == nil:
				counted = b.RevokedAt != nil
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
		}
		u, err := tx.Q.GetUserSafeguardForUpdate(ctx, t.UserID)
		userFound := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		id, _ := uuid.NewV7()
		uid, kid := t.UserID, t.KeyID
		evID, err := tx.Q.InsertSafeguardEvent(ctx, sqlc.InsertSafeguardEventParams{ID: id, RequestID: t.RequestID, UserID: &uid,
			KeyID: &kid, AccountID: t.AccountID, Model: t.Model, Category: t.Refusal.Category, Reason: Truncate(t.Refusal.Reason, 2000),
			CreatedAt: at, StatusCode: int32(t.StatusCode), Stream: t.Stream, Counted: counted, PromptHash: hash,
			Prompt: Truncate(prompt, PromptMaxRunes), BodyGz: gz, BodySize: int64(len(t.Body))})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already recorded
		}
		if err != nil {
			return err
		}
		out.EventID = evID
		if hash != "" {
			if err := tx.Q.UpsertSafeguardBlock(ctx, sqlc.UpsertSafeguardBlockParams{Hash: hash, Prompt: Truncate(prompt, PromptMaxRunes),
				FirstEventID: evID, FirstUserID: &uid, CreatedAt: at}); err != nil {
				return err
			}
		}
		if !counted || !userFound {
			return nil
		}
		def := set.SafeguardDefaultAllowance
		was := Locked(u.SafeguardUsed, u.SafeguardAllowance, def)
		after, err := tx.Q.SetUserSafeguard(ctx, sqlc.SetUserSafeguardParams{ID: t.UserID, Allowance: u.SafeguardAllowance,
			Used: u.SafeguardUsed + 1, ResetCards: u.SafeguardResetCards, DefaultAllowance: int32(def)})
		if err != nil {
			return err
		}
		out.Counted = true
		out.Locked = !was && Locked(after.SafeguardUsed, after.SafeguardAllowance, def)
		return tx.InvalidateUser(ctx, t.UserID)
	})
	if err != nil {
		return Recorded{}, err
	}
	if hash != "" && out.EventID != uuid.Nil {
		if err := s.St.R.SAdd(ctx, s.BlocksKey(), hash).Err(); err != nil {
			s.Log.Warn("add blocklist hash", "err", err)
		}
	}
	return out, nil
}

// Blocked reports whether hash is on the blocklist.
func (s *Service) Blocked(ctx context.Context, hash string) (bool, error) {
	if hash == "" {
		return false, nil
	}
	return s.St.R.SIsMember(ctx, s.BlocksKey(), hash).Result()
}

// RecordHit logs a request refused by the blocklist (it does not use the
// allowance).
func (s *Service) RecordHit(ctx context.Context, requestID, userID, keyID uuid.UUID, hash string, at time.Time) error {
	id, _ := uuid.NewV7()
	return s.Store.InTx(ctx, func(tx *store.Tx) error {
		kid := keyID
		if err := tx.Q.InsertSafeguardBlockedRequest(ctx, sqlc.InsertSafeguardBlockedRequestParams{ID: id, RequestID: requestID,
			UserID: userID, KeyID: &kid, Hash: hash, CreatedAt: at}); err != nil {
			return err
		}
		return tx.Q.CountSafeguardBlockHit(ctx, sqlc.CountSafeguardBlockHitParams{Hash: hash, At: at})
	})
}

// SyncBlocklist makes the Redis set equal to the active blocks in
// PostgreSQL (at startup and periodically; Record and Revoke update it
// right away).
func (s *Service) SyncBlocklist(ctx context.Context) error {
	hashes, err := s.Store.Q.ListActiveSafeguardBlockHashes(ctx)
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		want[h] = true
	}
	have, err := s.St.R.SMembers(ctx, s.BlocksKey()).Result()
	if err != nil {
		return err
	}
	var stale []any
	for _, h := range have {
		if !want[h] {
			stale = append(stale, h)
		}
	}
	pipe := s.St.R.Pipeline()
	if len(hashes) > 0 {
		add := make([]any, len(hashes))
		for i, h := range hashes {
			add[i] = h
		}
		pipe.SAdd(ctx, s.BlocksKey(), add...)
	}
	if len(stale) > 0 {
		pipe.SRem(ctx, s.BlocksKey(), stale...)
	}
	_, err = pipe.Exec(ctx)
	return err
}

// Revoke takes a hash off the blocklist.
func (s *Service) Revoke(ctx context.Context, hash string, by uuid.UUID) error {
	_, err := s.Store.Q.RevokeSafeguardBlock(ctx, sqlc.RevokeSafeguardBlockParams{Hash: hash, RevokedBy: &by})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.St.R.SRem(ctx, s.BlocksKey(), hash).Err()
}

// UserState is a user's safeguard counters.
type UserState = sqlc.SetUserSafeguardRow

// UseResetCard spends one of the user's reset cards to clear the count.
func (s *Service) UseResetCard(ctx context.Context, userID uuid.UUID) (UserState, error) {
	var out UserState
	err := s.Store.InTx(ctx, func(tx *store.Tx) error {
		u, err := tx.Q.GetUserSafeguardForUpdate(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if u.SafeguardResetCards <= 0 {
			return ErrNoResetCard
		}
		if u.SafeguardUsed == 0 {
			return ErrNothingToReset
		}
		out, err = tx.Q.SetUserSafeguard(ctx, sqlc.SetUserSafeguardParams{ID: userID, Allowance: u.SafeguardAllowance, Used: 0,
			ResetCards: u.SafeguardResetCards - 1, DefaultAllowance: int32(s.Settings.Get().SafeguardDefaultAllowance)})
		if err != nil {
			return err
		}
		return tx.InvalidateUser(ctx, userID)
	})
	return out, err
}

// Adjustment changes a user's counters; nil fields keep their value.
type Adjustment struct {
	// SetAllowance applies Allowance (nil = back to the default).
	SetAllowance bool
	Allowance    *int32
	Used         *int32
	ResetCards   *int32
}

// Adjust sets a user's allowance override, count or reset cards (admin).
// It returns the counters before and after.
func (s *Service) Adjust(ctx context.Context, userID uuid.UUID, a Adjustment) (before, after UserState, err error) {
	err = s.Store.InTx(ctx, func(tx *store.Tx) error {
		if _, err := tx.Q.GetUserSafeguardForUpdate(ctx, userID); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		u, err := tx.Q.GetUserSafeguard(ctx, userID)
		if err != nil {
			return err
		}
		before = UserState(u)
		p := sqlc.SetUserSafeguardParams{ID: userID, Allowance: u.SafeguardAllowance, Used: u.SafeguardUsed,
			ResetCards: u.SafeguardResetCards, DefaultAllowance: int32(s.Settings.Get().SafeguardDefaultAllowance)}
		if a.SetAllowance {
			p.Allowance = a.Allowance
		}
		if a.Used != nil {
			p.Used = *a.Used
		}
		if a.ResetCards != nil {
			p.ResetCards = *a.ResetCards
		}
		after, err = tx.Q.SetUserSafeguard(ctx, p)
		if err != nil {
			return err
		}
		return tx.InvalidateUser(ctx, userID)
	})
	return before, after, err
}

// SubmitAppeal files an unlock request of a locked user.
func (s *Service) SubmitAppeal(ctx context.Context, userID uuid.UUID, note string) (sqlc.SafeguardAppeal, error) {
	var out sqlc.SafeguardAppeal
	err := s.Store.InTx(ctx, func(tx *store.Tx) error {
		u, err := tx.Q.GetUserSafeguardForUpdate(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !Locked(u.SafeguardUsed, u.SafeguardAllowance, s.Settings.Get().SafeguardDefaultAllowance) {
			return ErrNotLocked
		}
		id, _ := uuid.NewV7()
		out, err = tx.Q.CreateSafeguardAppeal(ctx, sqlc.CreateSafeguardAppealParams{ID: id, UserID: userID, Note: note})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrAppealPending
			}
			return err
		}
		return nil
	})
	return out, err
}

// DecideAppeal approves (clearing the count) or rejects a pending appeal.
func (s *Service) DecideAppeal(ctx context.Context, appealID, by uuid.UUID, approve bool, reason string) (sqlc.SafeguardAppeal, error) {
	var out sqlc.SafeguardAppeal
	err := s.Store.InTx(ctx, func(tx *store.Tx) error {
		a, err := tx.Q.GetSafeguardAppealForUpdate(ctx, appealID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if a.Status != "pending" {
			return ErrAppealDecided
		}
		status := "rejected"
		if approve {
			status, reason = "approved", ""
		}
		out, err = tx.Q.DecideSafeguardAppeal(ctx, sqlc.DecideSafeguardAppealParams{ID: appealID, Status: status, RejectReason: reason,
			DecidedBy: &by})
		if err != nil {
			return err
		}
		if !approve {
			return nil
		}
		u, err := tx.Q.GetUserSafeguardForUpdate(ctx, a.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Q.SetUserSafeguard(ctx, sqlc.SetUserSafeguardParams{ID: a.UserID, Allowance: u.SafeguardAllowance, Used: 0,
			ResetCards: u.SafeguardResetCards, DefaultAllowance: int32(s.Settings.Get().SafeguardDefaultAllowance)}); err != nil {
			return err
		}
		return tx.InvalidateUser(ctx, a.UserID)
	})
	return out, err
}

// ---- bodies -------------------------------------------------------------------

// Gzip compresses a request body.
func Gzip(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(body); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Gunzip returns a reader of a stored body.
func Gunzip(gz []byte) (io.ReadCloser, error) {
	return gzip.NewReader(bytes.NewReader(gz))
}

// Chunk reads up to limit bytes of a stored body from offset, ending on a
// UTF-8 boundary (offset must be one: 0 or a previous next offset). next is
// -1 at the end of the body.
func Chunk(gz []byte, offset, limit int64) (string, int64, error) {
	zr, err := Gunzip(gz)
	if err != nil {
		return "", 0, err
	}
	defer zr.Close()
	if offset > 0 {
		if _, err := io.CopyN(io.Discard, zr, offset); err != nil {
			if errors.Is(err, io.EOF) {
				return "", -1, nil
			}
			return "", 0, err
		}
	}
	buf := make([]byte, limit+utf8.UTFMax)
	n, err := io.ReadFull(zr, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", 0, fmt.Errorf("read body: %w", err)
	}
	if int64(n) <= limit {
		return string(buf[:n]), -1, nil
	}
	end := int(limit)
	for end > 0 && !utf8.RuneStart(buf[end]) {
		end--
	}
	if end == 0 {
		end = int(limit)
	}
	return string(buf[:end]), offset + int64(end), nil
}
