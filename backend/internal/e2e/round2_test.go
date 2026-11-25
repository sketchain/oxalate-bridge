package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/tidwall/gjson"

	"github.com/sketchain/oxalate-bridge/backend/internal/fakecpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/testutil"
)

// errorCode returns error.code of a panel error body.
func errorCode(body map[string]any) string {
	if e, ok := body["error"].(map[string]any); ok {
		code, _ := e["code"].(string)
		return code
	}
	return ""
}

func TestClaudeOnlyPlansAndQuotaAccounts(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true})
	ctx := context.Background()

	// Plans: only claude.
	var body map[string]any
	if code := e.admin.do("POST", "/admin/plans", map[string]any{"name": "GPT", "subscription": "chatgpt",
		"weekly_quota_micro": 1, "price_micro": 0, "duration_value": 1, "duration_unit": "month"}, &body); code != 400 || errorCode(body) != "unsupported_subscription" {
		t.Fatalf("chatgpt plan: %d %v", code, body)
	}
	// Accounts: a cpa-quota instance that tracks no account (auth_indexes
	// unset and nothing seen yet) cannot be imported.
	empty := fakecpa.New("empty")
	t.Cleanup(empty.Close)
	empty.Set(func(c *fakecpa.CPA) { c.NoAccounts = true })
	body = nil
	if code := e.admin.do("POST", "/admin/accounts", map[string]any{"name": "E", "base_url": empty.URL(), "cpa_key": empty.Key,
		"quota_token": empty.QuotaToken}, &body); code != 422 || errorCode(body) != "upstream_validation_failed" ||
		!strings.Contains(fmt.Sprint(body), "quota response lists no account") {
		t.Fatalf("empty instance: %d %v", code, body)
	}
	if n := e.scalar("SELECT count(*) FROM accounts"); n != 1 {
		t.Fatalf("accounts %d", n)
	}
	// The subscription and provider columns are gone (cpa-quota v0.1.0
	// does not report them).
	if n := e.scalar("SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'accounts' AND column_name IN ('subscription','provider')"); n != 0 {
		t.Fatalf("stale columns %d", n)
	}

	// An existing account whose instance stops listing it: edits and tests
	// fail with the same reason.
	e.cpaA.Set(func(c *fakecpa.CPA) { c.NoAccounts = true })
	body = nil
	if code := e.admin.do("PATCH", "/admin/accounts/"+e.accA, map[string]any{"note": "x"}, &body); code != 422 || errorCode(body) != "upstream_validation_failed" {
		t.Fatalf("edit: %d %v", code, body)
	}
	res := e.admin.must("POST", "/admin/accounts/"+e.accA+"/test", nil, 200)
	if res["ok"] != false || res["quota_ok"] != false || !strings.Contains(res["quota_error"].(string), "lists no account") {
		t.Fatalf("test result %v", res)
	}
	_ = ctx
}

func TestModelsListIsUnionOfEnabledAccounts(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true, secondAcct: true})
	ctx := context.Background()
	e.cpaB.Set(func(c *fakecpa.CPA) { c.Models = []string{"claude-sonnet-4.6", "claude-haiku-4-5"} })
	testutil.Must(t, e.rep.App.Jobs.PollModels(ctx))
	testutil.Must(t, e.rep.App.Cat.ReloadRedis(ctx))
	models := func() []string {
		req, _ := http.NewRequest("GET", e.rep.URL()+"/v1/models", nil)
		req.Header.Set("X-Api-Key", e.key)
		resp, err := http.DefaultClient.Do(req)
		testutil.Must(t, err)
		defer resp.Body.Close()
		var out struct {
			Data []struct{ ID, Type string }
		}
		testutil.Must(t, json.NewDecoder(resp.Body).Decode(&out))
		var ids []string
		for _, m := range out.Data {
			ids = append(ids, m.ID)
		}
		return ids
	}
	got := strings.Join(models(), ",")
	if got != "claude-opus-4-8,claude-sonnet-4.6,claude-haiku-4-5" {
		t.Fatalf("models %s", got)
	}
	e.admin.must("PUT", "/admin/accounts/"+e.accB+"/enabled", map[string]any{"enabled": false}, 200)
	if got := strings.Join(models(), ","); got != "claude-opus-4-8,claude-sonnet-4.6" {
		t.Fatalf("models after disabling B: %s", got)
	}
}

func TestSessionInvalidation(t *testing.T) {
	e := setup(t, envOpts{noPlan: true})
	ctx := context.Background()
	st := e.rep.App.St
	sessions := func() int {
		n, err := st.R.SCard(ctx, st.UserSessionsKey(e.userID)).Result()
		testutil.Must(t, err)
		return int(n)
	}
	login := func(password string) *client {
		c := newClient(t, e.rep.URL())
		c.login("alice", password)
		return c
	}
	alive := func(c *client) bool { return c.do("GET", "/me", nil, nil) == 200 }

	// Self-service change: the current session stays, the others go.
	u1 := e.user
	u2 := login("alice-password")
	if sessions() != 2 {
		t.Fatalf("sessions %d", sessions())
	}
	u1.must("POST", "/me/password", map[string]any{"old_password": "alice-password", "new_password": "alice-password-2"}, 204)
	if !alive(u1) || alive(u2) || sessions() != 1 {
		t.Fatalf("after own change: u1 %v u2 %v sessions %d", alive(u1), alive(u2), sessions())
	}

	// Admin password reset: every session goes.
	u3 := login("alice-password-2")
	e.admin.must("PATCH", "/admin/users/"+e.userID.String(), map[string]any{"password": "alice-password-3"}, 200)
	if alive(u1) || alive(u3) || sessions() != 0 {
		t.Fatal("sessions survived an admin password reset")
	}
	// The admin's own session is untouched.
	if !alive(e.admin) {
		t.Fatal("admin session lost")
	}

	// Role change: every session goes.
	u4, u5 := login("alice-password-3"), login("alice-password-3")
	e.admin.must("PATCH", "/admin/users/"+e.userID.String(), map[string]any{"role": "admin"}, 200)
	if alive(u4) || alive(u5) || sessions() != 0 {
		t.Fatal("sessions survived a role change")
	}
	// Other edits (note, limits) keep sessions.
	u6 := login("alice-password-3")
	e.admin.must("PATCH", "/admin/users/"+e.userID.String(), map[string]any{"note": "vip", "max_concurrency": 5}, 200)
	if !alive(u6) {
		t.Fatal("unrelated edit dropped the session")
	}
	// Disabling: every session goes (the session key itself is deleted, not
	// just refused by the status check).
	tokens, err := st.UserSessions(ctx, e.userID)
	testutil.Must(t, err)
	e.admin.must("PATCH", "/admin/users/"+e.userID.String(), map[string]any{"status": "disabled"}, 200)
	for _, tok := range tokens {
		if n, _ := st.R.Exists(ctx, st.SessionKey(tok)).Result(); n != 0 {
			t.Fatal("session key survived disabling")
		}
	}
	if alive(u6) || sessions() != 0 {
		t.Fatal("session survived disabling")
	}
	// Logout removes the token from the set.
	e.admin.must("PATCH", "/admin/users/"+e.userID.String(), map[string]any{"status": "active"}, 200)
	u7 := login("alice-password-3")
	if sessions() != 1 {
		t.Fatalf("sessions %d", sessions())
	}
	u7.must("POST", "/auth/logout", nil, 204)
	if sessions() != 0 {
		t.Fatalf("logout left %d sessions", sessions())
	}
}

func TestAccountDeleteProtection(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, secondAcct: true})
	var body map[string]any
	if code := e.admin.do("DELETE", "/admin/accounts/"+e.accA, nil, &body); code != 409 || errorCode(body) != "account_has_subscriptions" {
		t.Fatalf("delete with active subscription: %d %v", code, body)
	}
	subs := body["error"].(map[string]any)["subscriptions"].([]any)
	if len(subs) != 1 {
		t.Fatalf("subscriptions %v", subs)
	}
	s := subs[0].(map[string]any)
	if s["id"] != e.subID || s["user_id"] != e.userID.String() || s["username"] != "alice" || s["status"] != "active" || s["plan_name"] != "Pro" {
		t.Fatalf("blocking subscription %v", s)
	}
	// Disabling is still allowed.
	e.admin.must("PUT", "/admin/accounts/"+e.accA+"/enabled", map[string]any{"enabled": false}, 200)

	// A pending subscription assigned to B blocks B; once cancelled B can go.
	bought := e.user.must("POST", "/plans/"+e.planID+"/purchase", map[string]any{}, 201)
	pending := bought["id"].(string)
	if bought["status"] != "pending_assignment" {
		t.Fatalf("purchase %v", bought)
	}
	e.admin.must("POST", "/admin/subscriptions/"+pending+"/assign", map[string]any{"account_id": e.accB}, 200)
	body = nil
	if code := e.admin.do("DELETE", "/admin/accounts/"+e.accB, nil, &body); code != 409 {
		t.Fatalf("delete B: %d %v", code, body)
	}
	e.admin.must("POST", "/admin/subscriptions/"+pending+"/cancel", map[string]any{"refund_micro": 0}, 200)
	e.admin.must("DELETE", "/admin/accounts/"+e.accB, nil, 204)

	// A, after its subscription is cancelled.
	e.admin.must("POST", "/admin/subscriptions/"+e.subID+"/cancel", map[string]any{"refund_micro": 0}, 200)
	e.admin.must("DELETE", "/admin/accounts/"+e.accA, nil, 204)
	// Binding to a deleted account fails.
	body = nil
	if code := e.admin.do("POST", "/admin/subscriptions", map[string]any{"user_id": e.userID, "plan_id": e.planID, "account_id": e.accA}, &body); code != 404 {
		t.Fatalf("grant on deleted account: %d %v", code, body)
	}
}

// cpaQuotaSchema creates the cpa-quota tables in a fresh schema and points
// the replica's reader at it.
func cpaQuotaSchema(t *testing.T, e *env, create bool) string {
	t.Helper()
	ctx := context.Background()
	cq := e.schema + "_cq"
	e.rep.App.API.CPAQuota.Schema = cq
	t.Cleanup(func() {
		_, _ = e.rep.App.Pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{cq}.Sanitize()+" CASCADE")
	})
	if !create {
		return cq
	}
	ddl, err := os.ReadFile("../cpaquota/schema.sql")
	testutil.Must(t, err)
	conn, err := e.rep.App.Pool.Acquire(ctx)
	testutil.Must(t, err)
	defer conn.Release()
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{cq}.Sanitize()+"; SET search_path TO "+pgx.Identifier{cq}.Sanitize()+"; "+
		string(ddl)+"; SET search_path TO "+pgx.Identifier{e.schema}.Sanitize()+", public")
	testutil.Must(t, err)
	return cq
}

func TestCPAQuotaEndpoints(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true})
	ctx := context.Background()
	base := "/admin/accounts/" + e.accA

	// Schema missing: 503 on every endpoint.
	cq := cpaQuotaSchema(t, e, false)
	from := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	to := from.Add(4 * time.Hour)
	q := "?from=" + from.Format(time.RFC3339) + "&to=" + to.Format(time.RFC3339)
	for _, path := range []string{"/estimate", "/k-trend", "/quota-series?window=5h", "/cost-compare" + q} {
		var body map[string]any
		if code := e.admin.do("GET", base+path, nil, &body); code != 503 || errorCode(body) != "cpa_quota_unavailable" {
			t.Fatalf("%s without schema: %d %v", path, code, body)
		}
	}
	// Admin only.
	if code := e.user.do("GET", base+"/estimate", nil, nil); code != 403 {
		t.Fatalf("user access %d", code)
	}

	cpaQuotaSchema(t, e, true)
	execArgs := func(sql string, args ...any) {
		t.Helper()
		tx, err := e.rep.App.Pool.Begin(ctx)
		testutil.Must(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{cq}.Sanitize())
		testutil.Must(t, err)
		_, err = tx.Exec(ctx, sql, args...)
		testutil.Must(t, err)
		testutil.Must(t, tx.Commit(ctx))
	}

	// Estimate: 404 until cpa-quota wrote one, then the row with detail as is.
	var body map[string]any
	if code := e.admin.do("GET", base+"/estimate", nil, &body); code != 404 {
		t.Fatalf("estimate before data: %d %v", code, body)
	}
	detail := `{"auth_index":"acct-a","k":6.0,"five_hour":{"used_percent":39,"total_usd":8.5},"nested":{"z":1,"a":[1,2]}}`
	execArgs(`INSERT INTO estimates (instance, auth_index, tier, state, basis, k, k_source,
		totals_from, safety_factor, used_5h, reset_5h, used_7d, five_hour_total_usd, weekly_total_usd, remaining_five_hour_usd,
		remaining_week_usd, clean_blocks, total_blocks, segments, excluded_segments, ratio, observed_at, estimated_at, detail)
		VALUES ('a', 'acct-a', 'max5x', 'estimated', 'raw', NULL, 'default', 'tier', 0.9, 39, now(), 39.83,
		8.5, 51, 4.6665, 27.6165, 6, 6, 228, 0, 11.76, now(), now(), $1)`, detail)
	est := e.admin.must("GET", base+"/estimate", nil, 200)
	gotDetail, _ := json.Marshal(est["detail"])
	if !jsonEqual(t, string(gotDetail), detail) || est["k"] != nil || est["tier"] != "max5x" || est["segments"].(float64) != 228 || est["reset_7d"] != nil {
		t.Fatalf("estimate %v", est)
	}

	// k trend: days window, oldest first.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, d := range []int{0, 5, 40} {
		execArgs(`INSERT INTO k_trend_daily (instance, auth_index, day, gain_5h, gain_7d, k, segments, excluded_segments, cost_usd, gain)
			VALUES ('a', 'acct-a', $1, 10, 1.6, 6.25, 12, 1, 0.9, 11)`, today.AddDate(0, 0, -d))
	}
	execArgs(`INSERT INTO k_trend_daily (instance, auth_index, day, gain_5h, gain_7d, k, segments, excluded_segments, cost_usd, gain)
		VALUES ('other', 'acct-a', $1, 1, 1, NULL, 1, 0, 0, 1)`, today)
	kt := e.admin.must("GET", base+"/k-trend", nil, 200)
	items := kt["items"].([]any)
	if kt["days"].(float64) != 30 || len(items) != 2 || items[1].(map[string]any)["day"] != today.Format("2006-01-02") {
		t.Fatalf("k trend %v", kt)
	}
	if kt := e.admin.must("GET", base+"/k-trend?days=41", nil, 200); len(kt["items"].([]any)) != 3 {
		t.Fatalf("k trend 41 days %v", kt)
	}
	for _, bad := range []string{"0", "366"} {
		if code := e.admin.do("GET", base+"/k-trend?days="+bad, nil, nil); code != 400 {
			t.Fatalf("days=%s: %d", bad, code)
		}
	}

	// Quota series: 2500 points one minute apart -> downsampled to <= 2000,
	// other limits and window lengths ignored.
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Minute)
	execArgs(`INSERT INTO quota_reports (instance, auth_index, request_id, at_ns)
		SELECT 'a', 'acct-a', 'r' || i, $1::bigint + i * 60000000000 FROM generate_series(0, 2499) AS i`, start.UnixNano())
	execArgs(`INSERT INTO quota_report_windows (report_id, instance, auth_index, limit_key, seconds, at_ns, used_percent, reset_ns)
		SELECT id, instance, auth_index, 'unified', 18000, at_ns, (id % 100)::double precision, at_ns + 3600000000000 FROM quota_reports`)
	execArgs(`INSERT INTO quota_report_windows (report_id, instance, auth_index, limit_key, seconds, at_ns, used_percent, reset_ns)
		SELECT id, instance, auth_index, 'unified', 604800, at_ns, 50, 0 FROM quota_reports WHERE request_id IN ('r10', 'r20')`)
	execArgs(`INSERT INTO quota_report_windows (report_id, instance, auth_index, limit_key, seconds, at_ns, used_percent, reset_ns)
		SELECT id, instance, auth_index, 'other', 18000, at_ns, 1, 0 FROM quota_reports WHERE request_id = 'r30'`)
	sFrom, sTo := start.Format(time.RFC3339), start.Add(2500*time.Minute).Format(time.RFC3339)
	qs := e.admin.must("GET", base+"/quota-series?window=5h&from="+sFrom+"&to="+sTo, nil, 200)
	pts := qs["items"].([]any)
	if qs["downsampled"] != true || qs["total_points"].(float64) != 2500 || len(pts) > 2000 || len(pts) < 1500 || qs["bucket_seconds"] == nil {
		t.Fatalf("downsampled series: %v points, %v", len(pts), qs["total_points"])
	}
	last := pts[len(pts)-1].(map[string]any)
	if lastAt, _ := time.Parse(time.RFC3339, last["at"].(string)); !lastAt.Equal(start.Add(2499 * time.Minute)) {
		t.Fatalf("last point %v", last)
	}
	// A shorter range is returned as is, with reset_at.
	qs = e.admin.must("GET", base+"/quota-series?window=5h&from="+sFrom+"&to="+start.Add(100*time.Minute).Format(time.RFC3339), nil, 200)
	pts = qs["items"].([]any)
	if qs["downsampled"] != false || len(pts) != 100 || pts[0].(map[string]any)["reset_at"] == nil || qs["bucket_seconds"] != nil {
		t.Fatalf("plain series %d %v", len(pts), qs["downsampled"])
	}
	if qs := e.admin.must("GET", base+"/quota-series?window=7d&from="+sFrom+"&to="+sTo, nil, 200); len(qs["items"].([]any)) != 2 {
		t.Fatalf("7d series %v", qs)
	}

	// Cost compare: one bridged request (3500 micro upstream) against two
	// complete cpa-quota requests ($0.004) and one inconsistent one.
	resp, out := e.relay("/v1/messages", []byte(`{"model":"claude-opus-4-8","max_tokens":8,"messages":[]}`), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("relay %d %s", resp.StatusCode, out)
	}
	e.persist()
	now := time.Now().UTC()
	for i, acc := range []string{"complete", "complete", "inconsistent"} {
		execArgs(`INSERT INTO requests (instance, auth_index, request_id, requested_ns, completed_ns, accounting, cost_usd)
			VALUES ('a', 'acct-a', $1, $2, $2, $3, $4)`, "q"+string(rune('0'+i)), now.UnixNano(), acc, map[bool]float64{true: 0.002, false: 1}[acc == "complete"])
	}
	cc := e.admin.must("GET", base+"/cost-compare"+q+"&bucket=hour", nil, 200)
	rows := cc["items"].([]any)
	if len(rows) != 1 {
		t.Fatalf("cost compare rows %v", cc)
	}
	r := rows[0].(map[string]any)
	if r["cpa_quota_cost_micro"].(float64) != 4000 || r["cpa_quota_requests"].(float64) != 2 || r["bridge_upstream_cost_micro"].(float64) != messagesCost ||
		r["bridge_requests"].(float64) != 1 || r["diff_micro"].(float64) != 4000-messagesCost || cc["diff_micro"].(float64) != 4000-messagesCost {
		t.Fatalf("cost compare %v", cc)
	}
	if b, _ := time.Parse(time.RFC3339, r["bucket"].(string)); !b.Equal(now.Truncate(time.Hour)) {
		t.Fatalf("bucket %v", r["bucket"])
	}
	if cc := e.admin.must("GET", base+"/cost-compare"+q+"&bucket=day", nil, 200); len(cc["items"].([]any)) == 0 {
		t.Fatalf("day buckets %v", cc)
	}
}

func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	testutil.Must(t, json.Unmarshal([]byte(a), &x))
	testutil.Must(t, json.Unmarshal([]byte(b), &y))
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}

func TestAvailabilityEndpoint(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000})
	ctx := context.Background()
	raw := func(c *client) (map[string]any, string) {
		var r json.RawMessage
		if code := c.do("GET", "/me/availability", nil, &r); code != 200 {
			t.Fatalf("availability %d %s", code, r)
		}
		var m map[string]any
		testutil.Must(t, json.Unmarshal(r, &m))
		return m, string(r)
	}
	av, text := raw(e.user)
	// Nothing about upstream accounts leaks.
	for _, leak := range []string{e.accA, e.cpaA.URL(), "acct-a", "accounts"} {
		if strings.Contains(text, leak) {
			t.Fatalf("availability leaks %q: %s", leak, text)
		}
	}
	if av["estimated"] != true || av["computed_at"] == nil || av["balance_available_micro"].(float64) != 1_000_000 {
		t.Fatalf("availability %s", text)
	}
	// Fake quota: 5h remaining 0.9 × 10 × 0.9 = $8.1; week remaining $72;
	// the plan reserves its $10 quota on the only account.
	normal := gjson.Get(text, "normal.#(horizon==\"1h\")")
	if normal.Get("pool_micro").Int() != 8_100_000 || normal.Get("available_micro").Int() != 1_000_000 || !normal.Get("limited_by_balance").Bool() {
		t.Fatalf("normal 1h %s", normal.Raw)
	}
	if f := gjson.Get(text, "force.#(horizon==\"1h\")"); f.Get("pool_micro").Int() != 8_100_000 {
		t.Fatalf("force 1h %s", f.Raw)
	}
	plans := gjson.Get(text, "plans").Array()
	if len(plans) != 1 {
		t.Fatalf("plans %s", text)
	}
	p := plans[0]
	if p.Get("status").String() != "active" || p.Get("account_status").String() != "normal" || p.Get("remaining_micro").Int() != 10_000_000 ||
		p.Get("five_hour.available_micro").Int() != 8_100_000 || p.Get("five_hour.reset_at").String() == "" ||
		p.Get("period.available_micro").Int() != 10_000_000 || p.Get("period.ends_at").String() == "" {
		t.Fatalf("plan %s", p.Raw)
	}
	// The cache was written on the miss; the leader refresh rewrites it.
	key := e.rep.App.St.Key("avail")
	if n, _ := e.rep.App.St.R.Exists(ctx, key).Result(); n != 1 {
		t.Fatal("availability not cached")
	}
	time.Sleep(10 * time.Millisecond)
	testutil.Must(t, e.rep.App.Jobs.PollQuotas(ctx))
	av2, _ := raw(e.user)
	t1, _ := time.Parse(time.RFC3339Nano, av["computed_at"].(string))
	t2, _ := time.Parse(time.RFC3339Nano, av2["computed_at"].(string))
	if !t2.After(t1) {
		t.Fatalf("leader refresh did not update the cache: %v -> %v", t1, t2)
	}
	// A pending subscription shows only its status.
	bought := e.user.must("POST", "/plans/"+e.planID+"/purchase", map[string]any{}, 201)
	_, text = raw(e.user)
	pend := gjson.Get(text, "plans.#(status==\"pending_assignment\")")
	if pend.Get("subscription_id").String() != bought["id"] || pend.Get("five_hour").Type != gjson.Null || pend.Get("remaining_micro").Type != gjson.Null {
		t.Fatalf("pending plan %s", pend.Raw)
	}
	// Admins get their own view.
	av3, _ := raw(e.admin)
	if len(av3["plans"].([]any)) != 0 || av3["balance_available_micro"].(float64) != 0 {
		t.Fatalf("admin view %v", av3)
	}
}
