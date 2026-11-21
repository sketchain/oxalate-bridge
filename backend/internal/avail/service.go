package avail

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/sketchain/oxalate-bridge/backend/internal/cpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/router"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
)

// cacheTTL bounds how long a snapshot survives without a refresh (e.g. no
// leader polling); after that the next request recomputes it.
const cacheTTL = 10 * time.Minute

// Service computes snapshots from the routing state and caches them in Redis.
type Service struct {
	Router *router.Router
	St     *rstate.State
	Log    *slog.Logger
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Service) key() string { return s.St.Key("avail") }

// Compute builds a snapshot from the current account states. It reads only
// the in-memory catalog and Redis (no upstream calls, no log scans).
func (s *Service) Compute(ctx context.Context) (*Snapshot, error) {
	now := s.now()
	states, err := s.Router.States(ctx, nil, now)
	if err != nil {
		return nil, err
	}
	// Local spend per window since its snapshot (or local reset).
	since5 := map[uuid.UUID]time.Time{}
	since7 := map[uuid.UUID]time.Time{}
	for id, st := range states {
		if st.Snapshot == nil || !st.Snapshot.HasData {
			continue
		}
		acc := st.Snapshot.Account
		observed := observedAt(st.Snapshot)
		if _, from, ok := cpa.EffectiveWindow(acc.FiveHour, acc.SafetyFactor, observed, now); ok {
			since5[id] = from
		}
		if _, from, ok := cpa.EffectiveWindow(acc.Week, acc.SafetyFactor, observed, now); ok {
			since7[id] = from
		}
	}
	spend5, err := s.St.SpendSinceMany(ctx, since5, now)
	if err != nil {
		return nil, err
	}
	spend7, err := s.St.SpendSinceMany(ctx, since7, now)
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(states))
	for _, a := range s.Router.Cat.Accounts() {
		st, ok := states[a.ID]
		if !ok {
			continue
		}
		accounts = append(accounts, FromState(st, spend5[a.ID], spend7[a.ID], now))
	}
	return Build(accounts, now), nil
}

func observedAt(snap *cpa.Snapshot) time.Time {
	if snap.Account.ObservedAt != nil {
		return *snap.Account.ObservedAt
	}
	return snap.FetchedAt
}

// StatusOf is the availability status of an account.
func StatusOf(st *router.AccountState) string {
	switch {
	case !st.Account.Enabled:
		return StatusDisabled
	case st.Account.Error:
		return StatusError
	case st.Cooldown != nil:
		return StatusCooldown
	case st.Stale:
		return StatusStale
	}
	return StatusNormal
}

// FromState derives an Account from its routing state and window spends.
func FromState(st *router.AccountState, spend5, spend7 int64, now time.Time) Account {
	out := Account{ID: st.Account.ID, Status: StatusOf(st), Reserved: st.Reserved}
	if st.Snapshot == nil || !st.Snapshot.HasData {
		return out
	}
	acc := st.Snapshot.Account
	exhausted := st.Cooldown != nil || cpa.Paused(acc, now)
	observed := observedAt(st.Snapshot)
	out.Five = WindowFrom(WindowInput{Window: acc.FiveHour, Safety: acc.SafetyFactor, Observed: observed, Spend: spend5,
		Exhausted: exhausted, Period: Period5h}, now)
	out.Week = WindowFrom(WindowInput{Window: acc.Week, Safety: acc.SafetyFactor, Observed: observed, Spend: spend7,
		Exhausted: exhausted, Period: Period7d}, now)
	return out
}

// Refresh computes a snapshot and stores it (the leader calls it after each
// quota poll).
func (s *Service) Refresh(ctx context.Context) (*Snapshot, error) {
	snap, err := s.Compute(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	if err := s.St.R.Set(ctx, s.key(), raw, cacheTTL).Err(); err != nil {
		return nil, err
	}
	return snap, nil
}

// Get returns the cached snapshot, computing and storing one when missing.
func (s *Service) Get(ctx context.Context) (*Snapshot, error) {
	raw, err := s.St.R.Get(ctx, s.key()).Bytes()
	switch {
	case err == nil:
		var snap Snapshot
		if json.Unmarshal(raw, &snap) == nil {
			return &snap, nil
		}
	case !errors.Is(err, redis.Nil):
		return nil, err
	}
	return s.Refresh(ctx)
}
