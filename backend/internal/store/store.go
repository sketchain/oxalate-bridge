// Package store wraps PostgreSQL transactions, the append-only ledger and the
// transactional outbox.
package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
)

// Store is the PostgreSQL access point.
type Store struct {
	Pool *pgxpool.Pool
	Q    *sqlc.Queries
	// Kick, when set, is called after a transaction that wrote outbox rows
	// commits, so the dispatcher delivers them without waiting for its poll.
	Kick func()
}

// New returns a store.
func New(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool, Q: sqlc.New(pool)}
}

// Tx is a transaction with outbox bookkeeping.
type Tx struct {
	Q      *sqlc.Queries
	Raw    pgx.Tx
	outbox bool
}

// InTx runs fn in a transaction.
func (s *Store) InTx(ctx context.Context, fn func(tx *Tx) error) error {
	raw, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	tx := &Tx{Q: s.Q.WithTx(raw), Raw: raw}
	if err := fn(tx); err != nil {
		_ = raw.Rollback(ctx)
		return err
	}
	if err := raw.Commit(ctx); err != nil {
		return err
	}
	if tx.outbox && s.Kick != nil {
		s.Kick()
	}
	return nil
}

// Outbox kinds.
const (
	OutboxAuthInvalidate = "auth_invalidate"
	OutboxWalletSync     = "wallet_sync"
	OutboxSettings       = "settings"
	OutboxRouting        = "routing"
)

// WalletSync is the payload of a wallet_sync outbox row: a committed
// absolute value and its version.
type WalletSync struct {
	Wallet  rstate.Wallet `json:"wallet"`
	Base    int64         `json:"base"`
	Version int64         `json:"version"`
}

// Enqueue writes an outbox row in the transaction.
func (tx *Tx) Enqueue(ctx context.Context, kind string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	tx.outbox = true
	return tx.Q.InsertOutbox(ctx, sqlc.InsertOutboxParams{Kind: kind, Payload: raw})
}

// InvalidateHashes enqueues an auth cache invalidation for raw key hashes.
func (tx *Tx) InvalidateHashes(ctx context.Context, hashes [][]byte) error {
	if len(hashes) == 0 {
		return nil
	}
	hexes := make([]string, len(hashes))
	for i, h := range hashes {
		hexes[i] = hex.EncodeToString(h)
	}
	return tx.Enqueue(ctx, OutboxAuthInvalidate, map[string]any{"hashes": hexes})
}

// InvalidateUser enqueues invalidation of every key of a user.
func (tx *Tx) InvalidateUser(ctx context.Context, user uuid.UUID) error {
	hashes, err := tx.Q.ListAPIKeyHashesByUser(ctx, user)
	if err != nil {
		return err
	}
	return tx.InvalidateHashes(ctx, hashes)
}

// RoutingChanged enqueues a catalog reload on every replica.
func (tx *Tx) RoutingChanged(ctx context.Context) error {
	return tx.Enqueue(ctx, OutboxRouting, map[string]any{})
}

// ErrInsufficientBalance is returned when a debit would overdraw.
var ErrInsufficientBalance = errors.New("insufficient balance")

// LedgerParams describes one ledger entry.
type LedgerParams struct {
	UserID         uuid.UUID
	Type           string
	Amount         int64
	RefType, RefID string
	IdempotencyKey string
	Note           string
	OperatorID     *uuid.UUID
	// MinAfter, when set, rejects the entry if the balance after it would be
	// below the value (e.g. 0 for purchases).
	MinAfter *int64
	// SyncRedis enqueues the new balance to the user's Redis wallet. The
	// settlement consumer syncs api_charge balances itself after commit.
	SyncRedis bool
}

// AppendLedger locks the user, appends an entry and updates the cached
// balance in the same transaction. It returns inserted=false when the
// idempotency key already exists.
func (tx *Tx) AppendLedger(ctx context.Context, p LedgerParams) (sqlc.LedgerEntry, bool, error) {
	e, inserted, _, err := tx.AppendLedgerVersion(ctx, p)
	return e, inserted, err
}

// AppendLedgerVersion is AppendLedger that also returns the new balance
// version.
func (tx *Tx) AppendLedgerVersion(ctx context.Context, p LedgerParams) (sqlc.LedgerEntry, bool, int64, error) {
	user, err := tx.Q.GetUserForUpdate(ctx, p.UserID)
	if err != nil {
		return sqlc.LedgerEntry{}, false, 0, fmt.Errorf("lock user: %w", err)
	}
	after := user.BalanceMicro + p.Amount
	if p.MinAfter != nil && after < *p.MinAfter {
		return sqlc.LedgerEntry{}, false, 0, ErrInsufficientBalance
	}
	id, _ := uuid.NewV7()
	entry, err := tx.Q.InsertLedgerEntry(ctx, sqlc.InsertLedgerEntryParams{ID: id, UserID: p.UserID, Type: p.Type,
		AmountMicro: p.Amount, BalanceAfterMicro: after, RefType: p.RefType, RefID: p.RefID,
		IdempotencyKey: p.IdempotencyKey, Note: p.Note, OperatorID: p.OperatorID})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.LedgerEntry{}, false, user.BalanceVersion, nil
	}
	if err != nil {
		return sqlc.LedgerEntry{}, false, 0, err
	}
	version, err := tx.Q.SetUserBalance(ctx, sqlc.SetUserBalanceParams{ID: p.UserID, BalanceMicro: after})
	if err != nil {
		return sqlc.LedgerEntry{}, false, 0, err
	}
	if p.SyncRedis {
		if err := tx.Enqueue(ctx, OutboxWalletSync, WalletSync{Wallet: rstate.UserWallet(p.UserID), Base: after, Version: version}); err != nil {
			return sqlc.LedgerEntry{}, false, 0, err
		}
	}
	return entry, true, version, nil
}
