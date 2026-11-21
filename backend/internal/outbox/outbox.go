// Package outbox delivers transactional outbox rows to Redis: auth cache
// invalidations, wallet deltas, settings and routing change notices.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/sketchain/oxalate-bridge/backend/internal/authcache"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
)

// Dispatcher delivers outbox rows. Every replica may run one; rows are
// claimed with FOR UPDATE SKIP LOCKED.
type Dispatcher struct {
	Store *store.Store
	St    *rstate.State
	Auth  *authcache.Cache
	Log   *slog.Logger
	kick  chan struct{}
}

// New returns a dispatcher.
func New(s *store.Store, st *rstate.State, auth *authcache.Cache, log *slog.Logger) *Dispatcher {
	return &Dispatcher{Store: s, St: st, Auth: auth, Log: log, kick: make(chan struct{}, 1)}
}

// Kick asks for an immediate delivery pass.
func (d *Dispatcher) Kick() {
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// Run delivers until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		for {
			n, err := d.DeliverOnce(ctx)
			if err != nil && ctx.Err() == nil {
				d.Log.Warn("outbox delivery", "err", err)
			}
			if n < 100 || err != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-d.kick:
		}
	}
}

// DeliverOnce claims and delivers up to 100 rows.
func (d *Dispatcher) DeliverOnce(ctx context.Context) (int, error) {
	n := 0
	err := d.Store.InTx(ctx, func(tx *store.Tx) error {
		rows, err := tx.Q.ClaimOutbox(ctx, 100)
		if err != nil {
			return err
		}
		n = len(rows)
		var done []int64
		for _, row := range rows {
			if err := d.deliver(ctx, row); err != nil {
				d.Log.Warn("outbox row failed", "id", row.ID, "kind", row.Kind, "err", err)
				if err := tx.Q.FailOutbox(ctx, sqlc.FailOutboxParams{ID: row.ID, LastError: err.Error()}); err != nil {
					return err
				}
				continue
			}
			done = append(done, row.ID)
		}
		if len(done) > 0 {
			return tx.Q.DeleteOutbox(ctx, done)
		}
		return nil
	})
	return n, err
}

func (d *Dispatcher) deliver(ctx context.Context, row sqlc.Outbox) error {
	switch row.Kind {
	case store.OutboxAuthInvalidate:
		var inv authcache.Invalidation
		if err := json.Unmarshal(row.Payload, &inv); err != nil {
			return err
		}
		return d.Auth.Deliver(ctx, inv)
	case store.OutboxWalletSync:
		var ws store.WalletSync
		if err := json.Unmarshal(row.Payload, &ws); err != nil {
			return err
		}
		return d.St.SyncBase(ctx, ws.Wallet, ws.Base, ws.Version)
	case store.OutboxSettings:
		return d.St.R.Publish(ctx, d.St.ChanSettings(), "reload").Err()
	case store.OutboxRouting:
		return d.St.R.Publish(ctx, d.St.ChanRouting(), "reload").Err()
	}
	return fmt.Errorf("unknown outbox kind %q", row.Kind)
}
