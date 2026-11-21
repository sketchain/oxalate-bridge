package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/sketchain/oxalate-bridge/backend/internal/fakecpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/testutil"
)

const opusBody = `{"model":"claude-opus-4-8","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`

func rawGet(t *testing.T, c *client, path string) string {
	t.Helper()
	var r json.RawMessage
	if code := c.do("GET", path, nil, &r); code != 200 {
		t.Fatalf("GET %s: %d %s", path, code, r)
	}
	return string(r)
}

// User-facing endpoints never carry upstream cost, multipliers or account
// fields; the admin views keep them.
func TestUserEndpointsHideUpstream(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 5_000_000, tiers: []string{"plan", "standard"}})
	for i := 0; i < 2; i++ {
		if resp, out := e.relay("/v1/messages", []byte(opusBody), nil); resp.StatusCode != 200 {
			t.Fatalf("relay %d %s", resp.StatusCode, out)
		}
	}
	e.persist()
	now := time.Now().UTC()
	from, to := now.Add(-48*time.Hour).Format(time.RFC3339), now.Add(48*time.Hour).Format(time.RFC3339)
	leaks := []string{"upstream_cost_micro", "tier_multiplier", "model_multiplier", "account_id", "used_upstream_micro",
		"session_hash", "client_ip", "retries", e.accA}
	for _, path := range []string{"/usage/logs", "/usage/summary?granularity=day&from=" + from + "&to=" + to, "/subscriptions",
		"/me/availability", "/keys", "/balance", "/ledger", "/plans", "/me"} {
		text := rawGet(t, e.user, path)
		for _, leak := range leaks {
			if strings.Contains(text, leak) {
				t.Fatalf("%s leaks %q: %s", path, leak, text)
			}
		}
	}
	logs := rawGet(t, e.user, "/usage/logs")
	if n := gjson.Get(logs, "items.#").Int(); n != 2 || gjson.Get(logs, "items.0.billed_micro").Int() <= 0 {
		t.Fatalf("user logs %s", logs)
	}
	sub := e.user.must("POST", "/plans/"+e.planID+"/purchase", map[string]any{}, 201)
	if _, ok := sub["account_id"]; ok {
		t.Fatalf("purchase returns account_id: %v", sub)
	}
	admin := rawGet(t, e.admin, "/admin/request-logs")
	for _, want := range []string{"upstream_cost_micro", "tier_multiplier", "account_id", "session_hash"} {
		if !strings.Contains(admin, want) {
			t.Fatalf("admin logs lack %q", want)
		}
	}
	summary := rawGet(t, e.admin, "/admin/usage/summary?granularity=day&from="+from+"&to="+to)
	if !strings.Contains(summary, "upstream_cost_micro") {
		t.Fatalf("admin summary lacks upstream cost: %s", summary)
	}
	// Admin subscription views carry the username and account name.
	subs := rawGet(t, e.admin, "/admin/subscriptions?user_id="+e.userID.String())
	active := gjson.Get(subs, `items.#(status=="active")`)
	if active.Get("username").String() != "alice" || active.Get("account_name").String() != "A" || active.Get("account_id").String() != e.accA {
		t.Fatalf("admin subscription %s", active.Raw)
	}
	pend := gjson.Get(subs, `items.#(status=="pending_assignment")`)
	if pend.Get("username").String() != "alice" || pend.Get("account_name").String() != "" {
		t.Fatalf("pending subscription %s", pend.Raw)
	}
}

// A disabled or failing account is "unavailable" to plan holders, with both
// plan figures at 0.
func TestPlanAvailabilityUnavailableAccount(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000})
	ctx := context.Background()
	check := func(want string, zero bool) {
		t.Helper()
		_, err := e.rep.App.Avail.Refresh(ctx)
		testutil.Must(t, err)
		text := rawGet(t, e.user, "/me/availability")
		p := gjson.Get(text, "plans.0")
		if p.Get("account_status").String() != want {
			t.Fatalf("account_status %s, want %s: %s", p.Get("account_status").String(), want, p.Raw)
		}
		five, per := p.Get("five_hour.available_micro").Int(), p.Get("period.available_micro").Int()
		if zero != (five == 0 && per == 0) {
			t.Fatalf("figures five=%d period=%d (zero=%v)", five, per, zero)
		}
	}
	check("normal", false)
	e.admin.must("PUT", "/admin/accounts/"+e.accA+"/enabled", map[string]any{"enabled": false}, 200)
	check("unavailable", true)
	// The admin view still distinguishes disabled from error.
	if st := gjson.Get(rawGet(t, e.admin, "/admin/accounts/"+e.accA), "runtime.status").String(); st != "disabled" {
		t.Fatalf("admin status %s", st)
	}
	e.admin.must("PUT", "/admin/accounts/"+e.accA+"/enabled", map[string]any{"enabled": true}, 200)
	_, err := e.rep.App.Pool.Exec(ctx, "UPDATE "+e.schema+".accounts SET error_status = true, error_reason = 'boom' WHERE id = $1", e.accA)
	testutil.Must(t, err)
	testutil.Must(t, e.rep.App.Cat.ReloadDB(ctx))
	check("unavailable", true)
	if st := gjson.Get(rawGet(t, e.admin, "/admin/accounts/"+e.accA), "runtime.status").String(); st != "error" {
		t.Fatalf("admin status %s", st)
	}
}

func TestAdminOverview(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 5_000_000, secondAcct: true, tiers: []string{"plan", "standard"}})
	ov := rawGet(t, e.admin, "/admin/overview")
	if gjson.Get(ov, "timezone").String() != "Asia/Shanghai" || gjson.Get(ov, "today.requests").Int() != 0 ||
		gjson.Get(ov, "last_reconcile").Type != gjson.Null || gjson.Get(ov, "pending_assignment").Int() != 0 ||
		gjson.Get(ov, "stream_backlog").Int() != 0 || gjson.Get(ov, "accounts.total").Int() != 2 {
		t.Fatalf("empty overview %s", ov)
	}
	// Two plan requests, then one standard after the plan is used up.
	for i := 0; i < 2; i++ {
		if resp, out := e.relay("/v1/messages", []byte(opusBody), nil); resp.StatusCode != 200 {
			t.Fatalf("relay %d %s", resp.StatusCode, out)
		}
	}
	if n := gjson.Get(rawGet(t, e.admin, "/admin/overview"), "stream_backlog").Int(); n != 2 {
		t.Fatalf("backlog before persisting %d", n)
	}
	e.persist()
	e.user.must("POST", "/plans/"+e.planID+"/purchase", map[string]any{}, 201)
	e.admin.must("PUT", "/admin/accounts/"+e.accB+"/enabled", map[string]any{"enabled": false}, 200)
	e.admin.must("POST", "/admin/reconcile", nil, 200)
	ov = rawGet(t, e.admin, "/admin/overview")
	if gjson.Get(ov, "today.requests").Int() != 2 || gjson.Get(ov, "today.billed_micro").Int() != 2*messagesCost ||
		gjson.Get(ov, "today.upstream_cost_micro").Int() != 2*messagesCost {
		t.Fatalf("today %s", gjson.Get(ov, "today").Raw)
	}
	tiers := gjson.Get(ov, "today.by_tier").Array()
	if len(tiers) != 1 || tiers[0].Get("tier").String() != "plan" || tiers[0].Get("requests").Int() != 2 {
		t.Fatalf("by tier %s", gjson.Get(ov, "today.by_tier").Raw)
	}
	acc := gjson.Get(ov, "accounts")
	if acc.Get("normal").Int() != 1 || acc.Get("disabled").Int() != 1 || acc.Get("reserved_micro").Int() <= 0 || acc.Get("headroom_micro").Int() <= 0 {
		t.Fatalf("accounts %s", acc.Raw)
	}
	if gjson.Get(ov, "pending_assignment").Int() != 1 || gjson.Get(ov, "stream_backlog").Int() != 0 ||
		!gjson.Get(ov, "last_reconcile.ok").Bool() {
		t.Fatalf("overview %s", ov)
	}
	// A 5-hour window above the watermark counts as penalized.
	e.cpaA.Set(func(c *fakecpa.CPA) { c.FiveUsed = 85 })
	testutil.Must(t, e.rep.App.Jobs.PollQuotas(context.Background()))
	ov = rawGet(t, e.admin, "/admin/overview")
	if gjson.Get(ov, "accounts.penalized").Int() != 1 || gjson.Get(ov, "accounts.normal").Int() != 0 {
		t.Fatalf("penalized %s", gjson.Get(ov, "accounts").Raw)
	}
	if !gjson.Get(rawGet(t, e.admin, "/admin/accounts/"+e.accA), "runtime.penalized").Bool() {
		t.Fatal("account runtime not penalized")
	}
	// Users cannot read it.
	if code := e.user.do("GET", "/admin/overview", nil, nil); code != 403 {
		t.Fatalf("user overview %d", code)
	}
}

func TestAccountValidateSoldAndKeyAdmin(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 5_000_000, planQuota: 30_000_000})
	// Validate is a dry run: nothing is saved.
	before := e.scalar("SELECT count(*) FROM " + e.schema + ".accounts")
	res := e.admin.must("POST", "/admin/accounts/validate", map[string]any{"base_url": e.cpaA.URL(), "cpa_key": e.cpaA.Key,
		"quota_token": e.cpaA.QuotaToken}, 200)
	if res["ok"] != true || res["instance"] != "a" || res["tier"] != "max5x" || res["auth_index"] != "acct-a" {
		t.Fatalf("validate %v", res)
	}
	bad := e.admin.must("POST", "/admin/accounts/validate", map[string]any{"base_url": e.cpaA.URL(), "cpa_key": "wrong",
		"quota_token": e.cpaA.QuotaToken}, 200)
	if bad["ok"] != false || bad["models_ok"] != false || bad["quota_ok"] != true {
		t.Fatalf("validate bad key %v", bad)
	}
	if after := e.scalar("SELECT count(*) FROM " + e.schema + ".accounts"); after != before {
		t.Fatalf("validate saved an account: %d -> %d", before, after)
	}
	// Sold quota and week total match the overcommit warning's figures.
	acc := rawGet(t, e.admin, "/admin/accounts/"+e.accA)
	if gjson.Get(acc, "sold_weekly_quota_micro").Int() != 30_000_000 || gjson.Get(acc, "sold_upstream_micro").Int() != 30_000_000 ||
		gjson.Get(acc, "week_total_micro").Int() != 100_000_000 {
		t.Fatalf("sold figures %s", acc)
	}
	// Admin key management: list the user's keys, disable one, the relay refuses it.
	keys := rawGet(t, e.admin, "/admin/users/"+e.userID.String()+"/keys")
	if gjson.Get(keys, "items.#").Int() != 1 || gjson.Get(keys, "items.0.id").String() != e.keyID || strings.Contains(keys, e.key) {
		t.Fatalf("admin keys %s", keys)
	}
	if resp, _ := e.relay("/v1/messages", []byte(opusBody), nil); resp.StatusCode != 200 {
		t.Fatalf("relay before disabling %d", resp.StatusCode)
	}
	k := e.admin.must("PUT", "/admin/keys/"+e.keyID+"/enabled", map[string]any{"enabled": false}, 200)
	if k["enabled"] != false {
		t.Fatalf("key %v", k)
	}
	e.waitOutbox()
	testutil.Eventually(t, 5*time.Second, func() bool {
		resp, _ := e.relay("/v1/messages", []byte(opusBody), nil)
		return resp.StatusCode == 401 || resp.StatusCode == 403
	}, "disabled key refused")
	audit := rawGet(t, e.admin, "/admin/audit-logs?target_type=api_key")
	if gjson.Get(audit, `items.#(action=="key.set_enabled")`).Type == gjson.Null {
		t.Fatalf("no audit entry: %s", audit)
	}
	e.admin.must("PUT", "/admin/keys/00000000-0000-7000-8000-000000000000/enabled", map[string]any{"enabled": false}, 404)
	// The status filter of the admin request logs.
	e.persist()
	ok := rawGet(t, e.admin, "/admin/request-logs?status_code=200")
	denied := rawGet(t, e.admin, "/admin/request-logs?status_code=599")
	if gjson.Get(ok, "items.#").Int() < 1 || gjson.Get(denied, "items.#").Int() != 0 {
		t.Fatalf("status filter: ok=%s denied=%s", ok, denied)
	}
}

// The account views carry k from the cpa-quota estimates when available.
func TestAccountK(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true})
	cq := cpaQuotaSchema(t, e, false)
	if k := gjson.Get(rawGet(t, e.admin, "/admin/accounts"), "items.0.k"); k.Type != gjson.Null {
		t.Fatalf("k without cpa-quota: %s", k.Raw)
	}
	cpaQuotaSchema(t, e, true)
	_, err := e.rep.App.Pool.Exec(context.Background(), `INSERT INTO `+cq+`.estimates (instance, auth_index, provider, subscription, tier, state,
		segment_window, basis, k, k_source, totals_from, safety_factor, used_5h, used_7d, five_hour_total_usd, weekly_total_usd,
		remaining_five_hour_usd, remaining_week_usd, clean_blocks, total_blocks, segments, excluded_segments, ratio, estimated_at, detail)
		VALUES ('a', 'acct-a', 'claude', 'claude', 'max5x', 'estimated', '5h', 'raw', 6.5, 'fit', 'tier', 0.9, 10, 20, 10, 100, 9, 72,
		1, 1, 1, 0, 1, now(), '{}')`)
	testutil.Must(t, err)
	if k := gjson.Get(rawGet(t, e.admin, "/admin/accounts"), "items.0.k").Float(); k != 6.5 {
		t.Fatalf("list k %v", k)
	}
	if k := gjson.Get(rawGet(t, e.admin, "/admin/accounts/"+e.accA), "k").Float(); k != 6.5 {
		t.Fatalf("detail k %v", k)
	}
}
