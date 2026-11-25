package notify

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
)

// State-change detection. The previous state lives in Redis so that a new
// leader (or a restarted replica) continues where the old one stopped and an
// event fires only on the transition, not on every poll.

func (s *Service) stateKey(parts ...string) string {
	return s.St.Key(append([]string{"push", "state"}, parts...)...)
}

func (s *Service) emit(ctx context.Context, n Notification) {
	if _, err := s.Notify(ctx, n); err != nil && ctx.Err() == nil {
		s.Log.Warn("notify", "event", n.Event, "err", err)
	}
}

// ---- accounts ---------------------------------------------------------------

// AccountObs is the notification-relevant state of one account.
type AccountObs struct {
	ID   uuid.UUID
	Name string
	// AuthError: the account is in error (upstream 401 / 403).
	AuthError bool
	Reason    string
	// Stale: the account had quota data and it is now too old.
	Stale bool
	Age   time.Duration
	// Full: "5h" or "week" when that window is used up, else "".
	Full string
}

type accountFlags struct {
	Auth  bool   `json:"a"`
	Stale bool   `json:"s"`
	Full  string `json:"f"`
}

// ObserveAccounts compares the accounts with their previous state and sends
// account_auth_error, account_stale and account_full on transitions.
func (s *Service) ObserveAccounts(ctx context.Context, obs []AccountObs) error {
	key := s.stateKey("accounts")
	prev, err := s.St.R.HGetAll(ctx, key).Result()
	if err != nil {
		return err
	}
	next := map[string]any{}
	for _, o := range obs {
		var p accountFlags
		if raw, ok := prev[o.ID.String()]; ok {
			_ = json.Unmarshal([]byte(raw), &p)
		}
		cur := accountFlags{Auth: o.AuthError, Stale: o.Stale, Full: o.Full}
		raw, _ := json.Marshal(cur)
		next[o.ID.String()] = string(raw)
		url := "/admin/accounts/" + o.ID.String()
		o := o
		if cur.Auth && !p.Auth {
			s.emit(ctx, Notification{Event: AccountAuthError, Object: o.ID.String(), URL: url,
				Params: func(l string) map[string]string {
					return map[string]string{"name": o.Name, "cause": phrase(l, "auth_"+AuthCause(o.Reason), nil), "reason": o.Reason}
				}})
		}
		if cur.Stale && !p.Stale {
			s.emit(ctx, Notification{Event: AccountStale, Object: o.ID.String(), URL: url,
				Params: func(l string) map[string]string {
					return map[string]string{"name": o.Name, "age": fmtAge(o.Age, l)}
				}})
		}
		if cur.Full != "" && p.Full == "" {
			s.emit(ctx, Notification{Event: AccountFull, Object: o.ID.String(), URL: url,
				Params: func(l string) map[string]string {
					return map[string]string{"name": o.Name, "window": phrase(l, "window_"+o.Full, nil)}
				}})
		}
	}
	if len(next) == 0 {
		return nil
	}
	pipe := s.St.R.TxPipeline()
	pipe.Del(ctx, key)
	pipe.HSet(ctx, key, next)
	_, err = pipe.Exec(ctx)
	return err
}

// ---- stream backlog and reconcile ---------------------------------------------

// ObserveBacklog sends stream_backlog_high once the backlog has stayed above
// threshold for BacklogHold; it re-arms when the backlog drops back.
func (s *Service) ObserveBacklog(ctx context.Context, backlog int64, threshold int) error {
	since, sent := s.stateKey("backlog", "since"), s.stateKey("backlog", "sent")
	if backlog <= int64(threshold) {
		return s.St.R.Del(ctx, since, sent).Err()
	}
	now := s.now()
	if _, err := s.St.R.SetNX(ctx, since, now.UnixMilli(), 0).Result(); err != nil {
		return err
	}
	ms, err := s.St.R.Get(ctx, since).Int64()
	if err != nil {
		return err
	}
	if now.Sub(time.UnixMilli(ms)) < BacklogHold {
		return nil
	}
	first, err := s.St.R.SetNX(ctx, sent, "1", 0).Result()
	if err != nil || !first {
		return err
	}
	s.emit(ctx, Notification{Event: StreamBacklogHigh, Object: "stream", URL: "/admin",
		Params: func(string) map[string]string {
			return map[string]string{"backlog": strconv.FormatInt(backlog, 10), "threshold": strconv.Itoa(threshold)}
		}})
	return nil
}

// ObserveReconcile sends reconcile_mismatch when a run fails after a good
// one (or the first run fails); a good run re-arms it.
func (s *Service) ObserveReconcile(ctx context.Context, ok bool, users int, diff int64) error {
	key := s.stateKey("reconcile_bad")
	if ok {
		return s.St.R.Del(ctx, key).Err()
	}
	first, err := s.St.R.SetNX(ctx, key, "1", 0).Result()
	if err != nil || !first {
		return err
	}
	s.emit(ctx, Notification{Event: ReconcileMismatch, Object: "reconcile", URL: "/admin/settings",
		Params: func(string) map[string]string {
			return map[string]string{"users": strconv.Itoa(users), "diff": usd(diff)}
		}})
	return nil
}

// ---- subscriptions --------------------------------------------------------------

// SubscriptionPending announces a new subscription waiting for an account.
func (s *Service) SubscriptionPending(ctx context.Context, sub sqlc.PlanSubscription) {
	username := ""
	if u, err := s.Store.Q.GetUser(ctx, sub.UserID); err == nil {
		username = u.Username
	}
	s.emit(ctx, Notification{Event: SubscriptionPending, Object: sub.ID.String(), URL: "/admin/subscriptions?tab=pending",
		Params: func(string) map[string]string { return map[string]string{"user": username, "plan": sub.PlanName} }})
}

// AutoRenewFailed tells the owner once per renewal period (subscription and
// expiry) that the automatic renewal did not go through.
func (s *Service) AutoRenewFailed(ctx context.Context, subID uuid.UUID, insufficient bool) {
	sub, err := s.Store.Q.GetSubscription(ctx, subID)
	if err != nil || sub.ExpiresAt == nil {
		return
	}
	ttl := time.Until(*sub.ExpiresAt) + 24*time.Hour
	if ttl < time.Hour {
		ttl = time.Hour
	}
	first, err := s.St.R.SetNX(ctx, s.stateKey("renew_failed", sub.ID.String(), strconv.FormatInt(sub.ExpiresAt.Unix(), 10)), "1", ttl).Result()
	if err != nil || !first {
		return
	}
	expires := *sub.ExpiresAt
	s.emit(ctx, Notification{Event: AutoRenewFailed, Object: sub.ID.String(), User: sub.UserID, URL: "/plans",
		Params: func(l string) map[string]string {
			reason := phrase(l, "renew_other", nil)
			if insufficient {
				reason = phrase(l, "renew_balance", nil)
			}
			return map[string]string{"plan": sub.PlanName, "date": fmtDate(expires, s.loc()), "reason": reason}
		}})
}

// ObservePlanExpiring sends plan_expiring for active subscriptions that
// expire within PlanExpiringLead, once per subscription and expiry (a renewal
// moves the expiry and starts a new period).
func (s *Service) ObservePlanExpiring(ctx context.Context) error {
	now := s.now()
	rows, err := s.Store.Q.ListSubscriptionsExpiring(ctx, now.Add(PlanExpiringLead))
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.ExpiresAt == nil {
			continue
		}
		key := s.stateKey("plan_expiring", r.ID.String(), strconv.FormatInt(r.ExpiresAt.Unix(), 10))
		first, err := s.St.R.SetNX(ctx, key, "1", r.ExpiresAt.Sub(now)+24*time.Hour).Result()
		if err != nil {
			return err
		}
		if !first {
			continue
		}
		r := r
		s.emit(ctx, Notification{Event: PlanExpiring, Object: r.ID.String() + ":" + strconv.FormatInt(r.ExpiresAt.Unix(), 10),
			User: r.UserID, URL: "/plans",
			Params: func(l string) map[string]string {
				renew := phrase(l, "renew_off", nil)
				if r.AutoRenew {
					renew = phrase(l, "renew_on", nil)
				}
				return map[string]string{"plan": r.PlanName, "date": fmtDate(*r.ExpiresAt, s.loc()), "renew": renew}
			}})
	}
	return nil
}

// ---- balances -------------------------------------------------------------------

// ObserveBalances sends balance_low when a user's settled balance falls from
// at or above their threshold to below it; it re-arms only after the balance
// is back at or above the threshold. Only users with a push device are
// checked (others cannot receive it).
func (s *Service) ObserveBalances(ctx context.Context) error {
	users, err := s.Store.Q.ListActiveUsersForNotify(ctx)
	if err != nil {
		return err
	}
	prefRows, err := s.Store.Q.ListNotificationPrefsForEvent(ctx, BalanceLow)
	if err != nil {
		return err
	}
	prefs := map[uuid.UUID]sqlc.NotificationPref{}
	for _, p := range prefRows {
		prefs[p.UserID] = p
	}
	key := s.stateKey("balance_low")
	low, err := s.St.R.SMembers(ctx, key).Result()
	if err != nil {
		return err
	}
	wasLow := map[string]bool{}
	for _, id := range low {
		wasLow[id] = true
	}
	sp, _ := SpecOf(BalanceLow)
	for _, u := range users {
		enabled, threshold := sp.DefaultEnabled, DefaultBalanceThreshold
		if p, ok := prefs[u.ID]; ok {
			enabled = p.Enabled
			if p.ThresholdMicro != nil {
				threshold = *p.ThresholdMicro
			}
		}
		id := u.ID.String()
		if !enabled {
			if wasLow[id] {
				_ = s.St.R.SRem(ctx, key, id).Err()
			}
			continue
		}
		bal, err := s.settledBalance(ctx, u.ID, u.BalanceMicro)
		if err != nil {
			return err
		}
		isLow := bal < threshold
		switch {
		case isLow && !wasLow[id]:
			if err := s.St.R.SAdd(ctx, key, id).Err(); err != nil {
				return err
			}
			th := threshold
			s.emit(ctx, Notification{Event: BalanceLow, Object: id, User: u.ID, URL: "/billing",
				Params: func(string) map[string]string {
					return map[string]string{"threshold": usd(th), "balance": usd(bal)}
				}})
		case !isLow && wasLow[id]:
			// Re-armed: the next drop below the threshold notifies again
			// (still subject to the dedup window).
			if err := s.St.R.SRem(ctx, key, id).Err(); err != nil {
				return err
			}
		}
	}
	return nil
}

// settledBalance is the balance after persisted and pending charges (holds
// of running requests excluded), from Redis, else PostgreSQL.
func (s *Service) settledBalance(ctx context.Context, user uuid.UUID, pg int64) (int64, error) {
	w := rstate.UserWallet(user)
	base, _, ok, err := s.St.Base(ctx, w)
	if err != nil || !ok {
		return pg, nil
	}
	pending, err := s.St.Pending(ctx, w)
	if err != nil {
		return pg, nil
	}
	return base - pending, nil
}

// ---- safeguard and keys -----------------------------------------------------------

// ObserveSafeguard announces safeguard events recorded since the last call,
// at most one per offending user within the dedup window.
func (s *Service) ObserveSafeguard(ctx context.Context) error {
	key := s.stateKey("safeguard_since")
	since := s.now()
	if ms, err := s.St.R.Get(ctx, key).Int64(); err == nil {
		since = time.UnixMilli(ms).UTC()
	} else if !errors.Is(err, redis.Nil) {
		return err
	} else {
		// First run: start from now, older events are history.
		return s.St.R.Set(ctx, key, since.UnixMilli(), 0).Err()
	}
	rows, err := s.Store.Q.ListSafeguardEventsSince(ctx, since)
	if err != nil {
		return err
	}
	for _, r := range rows {
		obj := "unknown"
		if r.UserID != nil {
			obj = r.UserID.String()
		}
		r := r
		s.emit(ctx, Notification{Event: SafeguardEvent, Object: obj, URL: "/admin/safeguard",
			Params: func(string) map[string]string { return map[string]string{"user": r.Username, "category": r.Category} }})
		since = r.CreatedAt
	}
	return s.St.R.Set(ctx, key, since.UnixMilli(), 0).Err()
}

// KeyExhausted tells the key owner that a key's total quota or a window is
// used up. It fires once per key, cause and window until the window should
// have recovered (a day for the total quota).
func (s *Service) KeyExhausted(ctx context.Context, keyID uuid.UUID, code, window string, recoversAt *time.Time) {
	ttl := 24 * time.Hour
	if recoversAt != nil {
		ttl = time.Until(*recoversAt)
		if ttl < time.Minute {
			ttl = time.Minute
		}
	}
	first, err := s.St.R.SetNX(ctx, s.stateKey("key_exhausted", keyID.String(), code, window), "1", ttl).Result()
	if err != nil || !first {
		return
	}
	k, err := s.Store.Q.GetAPIKey(ctx, keyID)
	if err != nil {
		return
	}
	s.emit(ctx, Notification{Event: KeyExhausted, Object: keyID.String(), User: k.UserID, URL: "/keys",
		Params: func(l string) map[string]string {
			name := k.Name
			if name == "" {
				name = k.KeyPrefix
			}
			what := phrase(l, "key_quota", nil)
			if window != "" {
				w := phrase(l, "window_"+window, nil)
				if w == "" {
					w = window
				}
				what = phrase(l, "key_window", map[string]string{"window": w})
			}
			rec := ""
			if recoversAt != nil {
				rec = " " + phrase(l, "key_recover", map[string]string{"date": fmtDate(*recoversAt, s.loc())})
			}
			return map[string]string{"key": name, "what": what, "recover": rec}
		}})
}
