// Package demo seeds a database with demonstration data for local
// development and the frontend smoke tests (`bridge seed-demo`): users,
// accounts pointing at cmd/fakecpa, plans, subscriptions (one waiting for an
// account), keys, live requests through the relay and 14 days of history.
package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sketchain/oxalate-bridge/backend/internal/db"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/jobs"
	"github.com/sketchain/oxalate-bridge/backend/internal/server"
	"github.com/sketchain/oxalate-bridge/backend/internal/settle"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
)

// Options configure a seed run.
type Options struct {
	// FakeCPA is the URL of the first cmd/fakecpa instance; instance i uses
	// the next ports (same layout as cmd/fakecpa -listen).
	FakeCPA string
	// Accounts is the number of fake instances to import (default 4).
	Accounts int
	// AdminUsername / AdminPassword log in as the bootstrap administrator.
	AdminUsername, AdminPassword string
	// UserPassword is the password of the demo users (default demo-pass-123).
	UserPassword string
	// Seed makes the generated history reproducible (0 = fixed default).
	Seed uint64
}

// Users are the demo users created by Seed.
var Users = []string{"alice", "bob", "carol"}

// ErrAlreadySeeded is returned when the demo users already exist.
var ErrAlreadySeeded = errors.New("demo data already present")

// Seed fills the database of a started app.
func Seed(ctx context.Context, app *server.App, log *slog.Logger, o Options) error {
	if o.Accounts <= 0 {
		o.Accounts = 4
	}
	if o.UserPassword == "" {
		o.UserPassword = "demo-pass-123"
	}
	if o.Seed == 0 {
		o.Seed = 20261121
	}
	if _, err := app.Store.Q.GetUserByUsername(ctx, Users[0]); err == nil {
		return ErrAlreadySeeded
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	s := &seeder{app: app, log: log, o: o, base: srv.URL, rng: rand.New(rand.NewPCG(o.Seed, o.Seed^0x9e3779b97f4a7c15))} //nolint:gosec // demo data
	return s.run(ctx)
}

type seeder struct {
	app  *server.App
	log  *slog.Logger
	o    Options
	base string
	rng  *rand.Rand

	admin    *client
	users    map[string]uuid.UUID
	clients  map[string]*client
	accounts []uuid.UUID
	plans    map[string]uuid.UUID
	subs     map[string]uuid.UUID
	keys     map[string]keyInfo
}

type keyInfo struct {
	id     uuid.UUID
	user   string
	secret string
	sub    *uuid.UUID
	tiers  []string
}

func (s *seeder) run(ctx context.Context) error {
	s.users, s.clients, s.plans, s.subs, s.keys = map[string]uuid.UUID{}, map[string]*client{}, map[string]uuid.UUID{}, map[string]uuid.UUID{}, map[string]keyInfo{}
	s.admin = newClient(s.base)
	if err := s.admin.login(s.o.AdminUsername, s.o.AdminPassword); err != nil {
		return fmt.Errorf("admin login (set BRIDGE_BOOTSTRAP_ADMIN_USERNAME/PASSWORD): %w", err)
	}
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"prices", s.prices},
		{"users", s.createUsers},
		{"accounts", s.importAccounts},
		{"plans", s.createPlans},
		{"subscriptions", s.createSubscriptions},
		{"keys", s.createKeys},
		{"live requests", s.liveRequests},
		{"history", s.history},
		{"settle", s.waitSettled},
		{"reconcile", s.reconcile},
		{"stale account", s.staleAccount},
	}
	for _, st := range steps {
		if err := st.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", st.name, err)
		}
		s.log.Info("seeded", "step", st.name)
	}
	return nil
}

func (s *seeder) fakeURL(i int) string {
	base := strings.TrimRight(s.o.FakeCPA, "/")
	idx := strings.LastIndex(base, ":")
	var port int
	if _, err := fmt.Sscanf(base[idx+1:], "%d", &port); err != nil {
		return base
	}
	return fmt.Sprintf("%s:%d", base[:idx], port+i-1)
}

// prices points price_url at the fake's ratio table and loads it.
func (s *seeder) prices(context.Context) error {
	if err := s.admin.call("PATCH", "/admin/settings", map[string]any{"price_url": s.fakeURL(1) + "/_fake/ratio_config"}, nil); err != nil {
		return err
	}
	return s.admin.call("POST", "/admin/prices/refresh", nil, nil)
}

func (s *seeder) createUsers(context.Context) error {
	specs := []struct {
		name    string
		topup   int64
		conc    int
		rpm     *int
		note    string
		balance string
	}{
		{"alice", 80_000_000, 6, nil, "演示用户：Plan + 余额", "80"},
		{"bob", 3_200_000, 3, ptr(60), "演示用户：余额较少", "3.2"},
		{"carol", 4_000, 2, nil, "演示用户：几乎没有余额", "0.004"},
	}
	for _, u := range specs {
		var out struct {
			ID uuid.UUID `json:"id"`
		}
		body := map[string]any{"username": u.name, "password": s.o.UserPassword, "max_concurrency": u.conc, "note": u.note}
		if u.rpm != nil {
			body["rpm"] = *u.rpm
		}
		if err := s.admin.call("POST", "/admin/users", body, &out); err != nil {
			return err
		}
		s.users[u.name] = out.ID
		if err := s.admin.call("POST", "/admin/users/"+out.ID.String()+"/balance", map[string]any{"type": "topup",
			"amount_micro": u.topup, "note": "演示充值 $" + u.balance}, nil); err != nil {
			return err
		}
		c := newClient(s.base)
		if err := c.login(u.name, s.o.UserPassword); err != nil {
			return err
		}
		s.clients[u.name] = c
	}
	return nil
}

var accountNames = []string{"claude-max-tokyo", "claude-max-osaka", "claude-max-seoul", "claude-pro-taipei"}

func (s *seeder) importAccounts(context.Context) error {
	for i := 1; i <= s.o.Accounts; i++ {
		name := fmt.Sprintf("claude-demo-%d", i)
		if i <= len(accountNames) {
			name = accountNames[i-1]
		}
		instance := fmt.Sprintf("demo-%d", i)
		var out struct {
			ID uuid.UUID `json:"id"`
		}
		if err := s.admin.call("POST", "/admin/accounts", map[string]any{"name": name, "base_url": s.fakeURL(i),
			"cpa_key": "cpa-key-" + instance, "quota_token": "quota-" + instance, "note": "fakecpa " + instance}, &out); err != nil {
			return fmt.Errorf("import %s (is cmd/fakecpa running at %s?): %w", instance, s.fakeURL(i), err)
		}
		s.accounts = append(s.accounts, out.ID)
	}
	return nil
}

func (s *seeder) createPlans(context.Context) error {
	specs := []map[string]any{
		{"name": "Max 周卡", "description": "每周 $60 额度，适合日常编码", "subscription": "claude", "weekly_quota_micro": 60_000_000,
			"price_micro": 18_000_000, "duration_value": 1, "duration_unit": "week", "allowed_models": []string{}, "sort_order": 1},
		{"name": "Max 月卡", "description": "每周 $120 额度，按月购买", "subscription": "claude", "weekly_quota_micro": 120_000_000,
			"price_micro": 99_000_000, "duration_value": 1, "duration_unit": "month", "allowed_models": []string{}, "sort_order": 2},
		{"name": "Sonnet 轻量包", "description": "仅 Sonnet，每周 $15 额度", "subscription": "claude", "weekly_quota_micro": 15_000_000,
			"price_micro": 4_500_000, "duration_value": 7, "duration_unit": "day", "allowed_models": []string{"claude-sonnet-4.6"}, "sort_order": 3},
	}
	for _, p := range specs {
		var out struct {
			ID uuid.UUID `json:"id"`
		}
		if err := s.admin.call("POST", "/admin/plans", p, &out); err != nil {
			return err
		}
		s.plans[p["name"].(string)] = out.ID
	}
	return nil
}

func (s *seeder) createSubscriptions(context.Context) error {
	alice := s.clients["alice"]
	// alice buys the weekly plan; the admin assigns it to the first account.
	var bought struct {
		ID uuid.UUID `json:"id"`
	}
	if err := alice.call("POST", "/plans/"+s.plans["Max 周卡"].String()+"/purchase", map[string]any{"auto_renew": true}, &bought); err != nil {
		return err
	}
	if err := s.admin.call("POST", "/admin/subscriptions/"+bought.ID.String()+"/assign", map[string]any{"account_id": s.accounts[0]}, nil); err != nil {
		return err
	}
	s.subs["alice"] = bought.ID
	// alice also buys the light plan, which stays waiting for an account.
	var pending struct {
		ID uuid.UUID `json:"id"`
	}
	if err := alice.call("POST", "/plans/"+s.plans["Sonnet 轻量包"].String()+"/purchase", nil, &pending); err != nil {
		return err
	}
	s.subs["alice-pending"] = pending.ID
	// bob gets the monthly plan from the admin on the second account.
	var granted struct {
		Subscription struct {
			ID uuid.UUID `json:"id"`
		} `json:"subscription"`
	}
	acc := s.accounts[min(1, len(s.accounts)-1)]
	if err := s.admin.call("POST", "/admin/subscriptions", map[string]any{"user_id": s.users["bob"], "plan_id": s.plans["Max 月卡"],
		"account_id": acc}, &granted); err != nil {
		return err
	}
	s.subs["bob"] = granted.Subscription.ID
	return nil
}

func (s *seeder) createKeys(context.Context) error {
	specs := []struct {
		label, user, name string
		tiers             []string
		sub               string
		conc, rpm         *int
	}{
		{"alice-cli", "alice", "Claude Code", []string{"plan", "standard"}, "alice", nil, nil},
		{"alice-batch", "alice", "批处理脚本", []string{"standard", "force"}, "", ptr(2), ptr(30)},
		{"bob-main", "bob", "默认", []string{"plan", "standard", "force"}, "bob", nil, nil},
		{"carol-main", "carol", "试用", []string{"standard"}, "", nil, nil},
	}
	for _, k := range specs {
		body := map[string]any{"name": k.name, "tiers": k.tiers}
		var sub *uuid.UUID
		if k.sub != "" {
			id := s.subs[k.sub]
			sub = &id
			body["subscription_id"] = id
		}
		if k.conc != nil {
			body["max_concurrency"] = *k.conc
		}
		if k.rpm != nil {
			body["rpm"] = *k.rpm
		}
		var out struct {
			ID  uuid.UUID `json:"id"`
			Key string    `json:"key"`
		}
		if err := s.clients[k.user].call("POST", "/keys", body, &out); err != nil {
			return err
		}
		s.keys[k.label] = keyInfo{id: out.ID, user: k.user, secret: out.Key, sub: sub, tiers: k.tiers}
	}
	return nil
}

// liveRequests sends real relay calls so the settlement pipeline, rollups and
// Redis wallets hold consistent recent data.
func (s *seeder) liveRequests(ctx context.Context) error {
	plan := []struct {
		key    string
		n      int
		model  string
		stream bool
	}{
		{"alice-cli", 8, "claude-opus-4-8", true},
		{"alice-cli", 4, "claude-sonnet-4.6", false},
		{"alice-batch", 4, "claude-sonnet-4.6", false},
		{"bob-main", 5, "claude-opus-4-8", true},
	}
	hc := &http.Client{Timeout: 60 * time.Second}
	for _, p := range plan {
		k := s.keys[p.key]
		for i := 0; i < p.n; i++ {
			body, _ := json.Marshal(map[string]any{"model": p.model, "max_tokens": 1024, "stream": p.stream,
				"metadata": map[string]any{"user_id": fmt.Sprintf("user_demo_account__session_%s-%d", p.key, i%2)},
				"messages": []map[string]any{{"role": "user", "content": "hello from seed-demo"}}})
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/v1/messages", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-api-key", k.secret)
			req.Header.Set("anthropic-version", "2023-06-01")
			req.Header.Set("User-Agent", "claude-cli/2.1.0 (seed-demo)")
			resp, err := hc.Do(req)
			if err != nil {
				return err
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				s.log.Warn("demo request not ok", "key", p.key, "status", resp.StatusCode)
			}
		}
	}
	return nil
}

// history writes 13 days of request logs and hourly rollups before today
// directly, with one api_charge ledger entry per user for the balance-tier
// part so reconciliation stays exact.
func (s *seeder) history(ctx context.Context) error {
	now := time.Now().UTC()
	if err := db.EnsurePartitions(ctx, s.app.Pool, now.AddDate(0, 0, -15), 1); err != nil {
		return err
	}
	type spec struct {
		key       string
		perDay    [2]int
		models    []string
		planShare float64
	}
	specs := []spec{
		{"alice-cli", [2]int{8, 22}, []string{"claude-opus-4-8", "claude-opus-4-8", "claude-sonnet-4.6"}, 0.75},
		{"alice-batch", [2]int{2, 8}, []string{"claude-sonnet-4.6"}, 0},
		{"bob-main", [2]int{1, 7}, []string{"claude-opus-4-8", "claude-sonnet-4.6"}, 0.6},
	}
	charges := map[string]int64{}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -13)
	err := s.app.Store.InTx(ctx, func(tx *store.Tx) error {
		for day := 0; day < 13; day++ {
			for _, sp := range specs {
				k := s.keys[sp.key]
				user := s.users[k.user]
				n := sp.perDay[0] + s.rng.IntN(sp.perDay[1]-sp.perDay[0]+1)
				for i := 0; i < n; i++ {
					at := start.AddDate(0, 0, day).Add(time.Duration(8*3600+s.rng.IntN(14*3600)) * time.Second)
					model := sp.models[s.rng.IntN(len(sp.models))]
					var balanceTiers []string
					for _, t := range k.tiers {
						if t != "plan" {
							balanceTiers = append(balanceTiers, t)
						}
					}
					tier := balanceTiers[s.rng.IntN(len(balanceTiers))]
					if k.sub != nil && s.rng.Float64() < sp.planShare {
						tier = "plan"
					}
					in := int64(200 + s.rng.IntN(6000))
					out := int64(80 + s.rng.IntN(3000))
					cr := int64(s.rng.IntN(60000))
					cw := int64(s.rng.IntN(8000))
					price := 15.0
					if strings.Contains(model, "sonnet") {
						price = 3.0
					}
					upstream := int64((float64(in)*price + float64(out)*price*5 + float64(cr)*price*0.1 + float64(cw)*price*1.25))
					status, errType := int32(200), ""
					if s.rng.IntN(40) == 0 {
						status, errType, upstream, out, cr, cw = 529, "overloaded_error", 0, 0, 0, 0
					}
					billed := upstream
					acc := s.accounts[0]
					if k.user == "bob" {
						acc = s.accounts[min(1, len(s.accounts)-1)]
					} else if tier != "plan" {
						acc = s.accounts[s.rng.IntN(len(s.accounts))]
					}
					var sub *uuid.UUID
					if tier == "plan" {
						sub = k.sub
					}
					id, _ := uuid.NewV7()
					latency := int32(800 + s.rng.IntN(20000))
					if _, err := tx.Q.InsertRequestLog(ctx, sqlc.InsertRequestLogParams{RequestID: id, CreatedAt: at, UserID: &user,
						KeyID: &k.id, AccountID: &acc, Tier: tier, SubscriptionID: sub, Endpoint: "/v1/messages", ModelRaw: model,
						Model: model, Stream: s.rng.IntN(3) > 0, StatusCode: status, ErrorType: errType, InputTokens: in,
						CacheReadTokens: cr, CacheWriteTokens: cw, OutputTokens: out, UpstreamCostMicro: upstream, BilledMicro: billed,
						TierMultiplier: 1, ModelMultiplier: 1, LatencyMs: latency, FirstByteMs: int32(300 + s.rng.IntN(2500)),
						Retries: int32(boolInt(s.rng.IntN(15) == 0)), SessionHash: fmt.Sprintf("%016x", s.rng.Uint64()),
						ClientIp: "203.0.113." + fmt.Sprint(10+s.rng.IntN(40)), UserAgent: "claude-cli/2.1.0 (demo history)"}); err != nil {
						return err
					}
					errs := int64(0)
					if status != 200 {
						errs = 1
					}
					if err := tx.Q.AddUsageHourly(ctx, sqlc.AddUsageHourlyParams{Hour: at.Truncate(time.Hour), UserID: user, KeyID: k.id,
						AccountID: acc, Model: model, Tier: tier, Requests: 1, Errors: errs, InputTokens: in, CacheReadTokens: cr,
						CacheWriteTokens: cw, OutputTokens: out, UpstreamCostMicro: upstream, BilledMicro: billed}); err != nil {
						return err
					}
					if tier == "standard" || tier == "force" {
						charges[k.user] += billed
					}
				}
			}
		}
		for user, amount := range charges {
			if amount == 0 {
				continue
			}
			if _, _, err := tx.AppendLedger(ctx, store.LedgerParams{UserID: s.users[user], Type: "api_charge", Amount: -amount,
				RefType: "settle_batch", RefID: "demo-history", IdempotencyKey: "api_charge:demo-history:" + user,
				Note: "演示历史用量", SyncRedis: true}); err != nil {
				return err
			}
		}
		// One safeguard event so the table is not empty.
		id, _ := uuid.NewV7()
		rid, _ := uuid.NewV7()
		alice := s.users["alice"]
		k := s.keys["alice-cli"]
		return tx.Q.InsertSafeguardEvent(ctx, sqlc.InsertSafeguardEventParams{ID: id, RequestID: rid, UserID: &alice, KeyID: &k.id,
			AccountID: &s.accounts[0], Model: "claude-opus-4-8", Category: "cyber", Reason: "演示：请求被上游安全策略拒绝",
			CreatedAt: now.Add(-26 * time.Hour)})
	})
	return err
}

// waitSettled waits until the settlement stream is drained.
func (s *seeder) waitSettled(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		groups, err := s.app.Redis.XInfoGroups(ctx, s.app.St.StreamKey()).Result()
		if err == nil {
			for _, g := range groups {
				if g.Name == settle.Group && g.Lag == 0 && g.Pending == 0 {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return errors.New("settlement stream not drained after 30s")
}

func (s *seeder) reconcile(ctx context.Context) error {
	res, err := jobs.Reconcile(ctx, s.app.Store, s.log)
	if err != nil {
		return err
	}
	if !res.OK {
		s.log.Warn("reconcile after seeding is not ok", "result", res)
	}
	return nil
}

// staleAccount makes the last fake instance fail /quota so its account goes
// stale after stale_after_seconds (the panel then shows a stale account).
func (s *seeder) staleAccount(ctx context.Context) error {
	if s.o.Accounts < 4 {
		return nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.fakeURL(s.o.Accounts)+"/_fake/quota-status?code=503", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// ---- tiny API client ---------------------------------------------------------------

type client struct {
	base string
	hc   *http.Client
}

func newClient(base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{base: base, hc: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
}

func (c *client) login(username, password string) error {
	return c.call("POST", "/auth/login", map[string]any{"username": username, "password": password}, nil)
}

func (c *client) call(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+"/api/v1"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "seed-demo")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, raw)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
