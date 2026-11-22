package router

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/sketchain/oxalate-bridge/backend/internal/authcache"
	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/settings"
)

// Tier names.
const (
	TierPlan     = "plan"
	TierStandard = "standard"
	TierForce    = "force"
)

// Rejection explains why a tier did not admit a request.
type Rejection struct {
	Tier    string `json:"tier"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// Window and RecoversAt are set for key_balance_quota_exhausted caused
	// by a window.
	Window     string     `json:"window,omitempty"`
	RecoversAt *time.Time `json:"recovers_at,omitempty"`
}

func reject(tier, code, msg string) *Rejection {
	return &Rejection{Tier: tier, Code: code, Message: msg}
}

// Rejection codes.
const (
	CodeNoSubscription    = "no_subscription"
	CodeSubInactive       = "subscription_inactive"
	CodeSubExpired        = "subscription_expired"
	CodeModelNotInPlan    = "model_not_in_plan"
	CodePlanExhausted     = "plan_quota_exhausted"
	CodePlanAccountDown   = "plan_account_unavailable"
	CodeInsufficientFunds = "insufficient_balance"
	CodeNoAccount         = "no_available_account"
	CodeAccountBusy       = "account_concurrency_full"
	CodeUnsupportedTier   = "unsupported_tier"
	CodeModelNotServed    = "model_not_served"
	CodeStateUnavailable  = "state_unavailable"
)

// Router evaluates tiers and picks accounts.
type Router struct {
	Cat      *Catalog
	St       *rstate.State
	Settings *settings.Store
	Rand     func() float64
}

// New returns a router.
func New(cat *Catalog, st *rstate.State, s *settings.Store) *Router {
	return &Router{Cat: cat, St: st, Settings: s, Rand: rand.Float64}
}

// PlanTarget is an admitted plan request.
type PlanTarget struct {
	Sub     *Subscription
	Account *Account
	Period  Period
	Wallet  rstate.Wallet
}

// EvalPlan checks the plan tier for a key and model. The period quota itself
// is checked atomically by the hold.
func (r *Router) EvalPlan(ctx context.Context, auth authcache.Info, normModel string, now time.Time) (*PlanTarget, *Rejection) {
	if auth.SubscriptionID == nil {
		return nil, reject(TierPlan, CodeNoSubscription, "key 未绑定 plan 订阅")
	}
	sub, ok := r.Cat.Subscription(*auth.SubscriptionID)
	if !ok || sub.Status != "active" || sub.UserID != auth.UserID {
		return nil, reject(TierPlan, CodeSubInactive, "plan 订阅未生效")
	}
	if sub.ExpiresAt == nil || !now.Before(*sub.ExpiresAt) {
		return nil, reject(TierPlan, CodeSubExpired, "plan 订阅已过期")
	}
	if len(sub.AllowedModels) > 0 && !sub.AllowedModels[normModel] {
		return nil, reject(TierPlan, CodeModelNotInPlan, "模型不在 plan 内")
	}
	if sub.AccountID == nil {
		return nil, reject(TierPlan, CodePlanAccountDown, "plan 订阅未分配账号")
	}
	acc, ok := r.Cat.Account(*sub.AccountID)
	if !ok || !acc.Enabled || acc.Error {
		return nil, reject(TierPlan, CodePlanAccountDown, "plan 绑定的账号不可用")
	}
	if !r.Cat.Serves(acc.ID, normModel) {
		return nil, reject(TierPlan, CodeModelNotServed, "plan 绑定的账号不提供该模型")
	}
	if exhausted, why := r.hardExhausted(ctx, acc.ID, now); exhausted {
		return nil, reject(TierPlan, CodePlanAccountDown, "plan 绑定的账号额度已用尽（"+why+"）")
	}
	period := r.Cat.PeriodFor(ctx, sub, now)
	return &PlanTarget{Sub: sub, Account: acc, Period: period, Wallet: rstate.PeriodWallet(sub.ID, period.Start)}, nil
}

func (r *Router) hardExhausted(ctx context.Context, account uuid.UUID, now time.Time) (bool, string) {
	if cd, ok := r.St.Cooldown(ctx, account); ok && now.Before(cd.Until) {
		return true, "cooldown"
	}
	snap := r.Cat.Snapshot(account)
	if snap == nil || !snap.HasData {
		return false, ""
	}
	return cpa.HardExhausted(snap.Account, now)
}

// Candidate is an account eligible for a balance tier.
type Candidate struct {
	Account  *Account
	Weight   float64
	Headroom int64
}

// AccountState is the computed live state of one account.
type AccountState struct {
	Account       *Account
	Snapshot      *cpa.Snapshot
	Stale         bool
	Cooldown      *rstate.CooldownInfo
	HardExhausted bool
	HardReason    string
	Spend         int64 // S_A
	Reserved      int64
	Headroom      int64 // H_A
	HasWeek       bool
	Weight        float64
}

// States computes the live state of the given accounts (all when nil).
func (r *Router) States(ctx context.Context, accounts []*Account, now time.Time) (map[uuid.UUID]*AccountState, error) {
	if accounts == nil {
		accounts = r.Cat.Accounts()
	}
	set := r.Settings.Get()
	ids := make([]uuid.UUID, len(accounts))
	for i, a := range accounts {
		ids[i] = a.ID
	}
	cooldowns := r.St.Cooldowns(ctx, ids)
	staleAfter := time.Duration(set.StaleAfterSeconds) * time.Second
	planMult := set.TierMultipliers.Plan

	out := make(map[uuid.UUID]*AccountState, len(accounts))
	since := map[uuid.UUID]time.Time{}
	type subWallet struct {
		account uuid.UUID
		wallet  rstate.Wallet
		quota   int64
	}
	var wallets []subWallet
	for _, a := range accounts {
		st := &AccountState{Account: a, Snapshot: r.Cat.Snapshot(a.ID)}
		st.Stale = st.Snapshot.Stale(now, staleAfter)
		if cd, ok := cooldowns[a.ID]; ok && now.Before(cd.Until) {
			cd := cd
			st.Cooldown = &cd
			st.HardExhausted, st.HardReason = true, "cooldown"
		}
		if st.Snapshot != nil && st.Snapshot.HasData {
			acc := st.Snapshot.Account
			if !st.HardExhausted {
				st.HardExhausted, st.HardReason = cpa.HardExhausted(acc, now)
			}
			observed := st.Snapshot.FetchedAt
			if acc.ObservedAt != nil {
				observed = *acc.ObservedAt
			}
			if _, from, ok := cpa.EffectiveWindow(acc.Week, acc.SafetyFactor, observed, now); ok {
				st.HasWeek = true
				since[a.ID] = from
			}
			wm := set.Watermark5hPercent
			if a.Watermark5hPercent != nil {
				wm = *a.Watermark5hPercent
			}
			st.Weight = Weight(st.Snapshot, now, wm, set.WatermarkPenalty)
		} else {
			st.Weight = 1
		}
		for _, sub := range r.Cat.SubscriptionsOn(a.ID) {
			if sub.ExpiresAt != nil && !now.Before(*sub.ExpiresAt) {
				continue
			}
			p := r.Cat.PeriodFor(ctx, sub, now)
			wallets = append(wallets, subWallet{account: a.ID, wallet: rstate.PeriodWallet(sub.ID, p.Start), quota: sub.WeeklyQuota})
		}
		out[a.ID] = st
	}
	spend, err := r.St.SpendSinceMany(ctx, since, now)
	if err != nil {
		return nil, err
	}
	ws := make([]rstate.Wallet, len(wallets))
	for i, w := range wallets {
		ws[i] = w.wallet
	}
	avail, err := r.St.AvailableMany(ctx, ws, now)
	if err != nil {
		return nil, err
	}
	for _, w := range wallets {
		remaining, ok := avail[w.wallet]
		if !ok {
			remaining = w.quota // untouched period: full quota still claimed
		}
		out[w.account].Reserved += Reservation(remaining, planMult)
	}
	for id, st := range out {
		st.Spend = spend[id]
		if st.HasWeek {
			acc := st.Snapshot.Account
			observed := st.Snapshot.FetchedAt
			if acc.ObservedAt != nil {
				observed = *acc.ObservedAt
			}
			week, _, _ := cpa.EffectiveWindow(acc.Week, acc.SafetyFactor, observed, now)
			st.Headroom = Headroom(HeadroomInput{WeekRemainingMicro: billing.USDToMicro(week.RemainingUSD), Spend: st.Spend, Reserved: st.Reserved})
		}
	}
	return out, nil
}

// Candidates returns the accounts eligible for a balance tier.
func (r *Router) Candidates(ctx context.Context, tier, normModel string, now time.Time, exclude map[uuid.UUID]bool) ([]Candidate, error) {
	var pool []*Account
	for _, a := range r.Cat.Accounts() {
		if !a.Enabled || a.Error || exclude[a.ID] || !r.Cat.Serves(a.ID, normModel) {
			continue
		}
		pool = append(pool, a)
	}
	if len(pool) == 0 {
		return nil, nil
	}
	states, err := r.States(ctx, pool, now)
	if err != nil {
		return nil, err
	}
	minHeadroom := r.Settings.Get().StandardMinHeadroomMicro
	var out []Candidate
	for _, a := range pool {
		st := states[a.ID]
		if st.HardExhausted {
			continue
		}
		if tier == TierStandard {
			if st.Stale || !st.HasWeek || st.Headroom <= minHeadroom {
				continue
			}
		}
		out = append(out, Candidate{Account: a, Weight: st.Weight, Headroom: st.Headroom})
	}
	return out, nil
}

// Pick chooses among candidates: the sticky account when it is a candidate,
// otherwise a weighted random pick.
func (r *Router) Pick(cands []Candidate, sticky uuid.UUID) (Candidate, bool) {
	if len(cands) == 0 {
		return Candidate{}, false
	}
	if sticky != uuid.Nil {
		for _, c := range cands {
			if c.Account.ID == sticky {
				return c, true
			}
		}
	}
	weights := make([]float64, len(cands))
	for i, c := range cands {
		weights[i] = c.Weight
	}
	return cands[PickWeighted(weights, r.Rand())], true
}

// AffinityTTL is the sticky mapping TTL for a request.
func (r *Router) AffinityTTL(oneHourCache bool) time.Duration {
	s := r.Settings.Get()
	if oneHourCache {
		return time.Duration(s.StickyTTL1hSeconds) * time.Second
	}
	return time.Duration(s.StickyTTLSeconds) * time.Second
}

// ServedModels lists the distinct model ids served by usable accounts.
func (r *Router) ServedModels() []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range r.Cat.Accounts() {
		if !a.Enabled || a.Error {
			continue
		}
		for _, m := range r.Cat.ModelIDs(a.ID) {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}
