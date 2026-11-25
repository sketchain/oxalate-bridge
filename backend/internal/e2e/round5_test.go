package e2e

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"

	"github.com/sketchain/oxalate-bridge/backend/internal/fakecpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/server"
	"github.com/sketchain/oxalate-bridge/backend/internal/testutil"
)

// patchKey edits the env user's key and waits until the cache saw it.
func (e *env) patchKey(body map[string]any) map[string]any {
	e.t.Helper()
	out := e.user.must("PATCH", "/keys/"+e.keyID, body, 200)
	e.waitOutbox()
	return out
}

func (e *env) relayOpus(headers map[string]string) (*http.Response, []byte) {
	return e.relay("/v1/messages", []byte(opusBody), headers)
}

func errCode(raw []byte) string { return gjson.GetBytes(raw, "error.code").String() }

func TestKeyQuotaScopeAll(t *testing.T) {
	// Plan quota is plenty; the key quota (all) also counts plan charges.
	e := setup(t, envOpts{balanceMicro: 1_000_000, tiers: []string{"plan", "standard"}})
	e.patchKey(map[string]any{"quota_limit_micro": 5000, "quota_scope": "all"})
	for i := 0; i < 2; i++ {
		resp, out := e.relayOpus(nil)
		if resp.StatusCode != 200 || resp.Header.Get("X-Relay-Tier") != "plan" {
			t.Fatalf("request %d: %d %s", i, resp.StatusCode, out)
		}
	}
	// 7000 used ≥ 5000: refused at the key, never falls through to standard.
	resp, out := e.relayOpus(nil)
	if resp.StatusCode != http.StatusTooManyRequests || errCode(out) != "key_quota_exhausted" ||
		gjson.GetBytes(out, "error.type").String() != "rate_limit_error" || gjson.GetBytes(out, "type").String() != "error" {
		t.Fatalf("exhausted: %d %s", resp.StatusCode, out)
	}
	lim := gjson.Parse(rawGet(t, e.user, "/keys/"+e.keyID+"/limits"))
	if lim.Get("quota_used_micro").Int() != 2*messagesCost || lim.Get("quota_remaining_micro").Int() != 5000-2*messagesCost {
		t.Fatalf("limits %s", lim.Raw)
	}
	e.persist()
	if n := e.scalar("SELECT count(*) FROM request_logs WHERE error_type = 'key_quota_exhausted' AND status_code = 429"); n != 1 {
		t.Fatalf("rejected logs %d", n)
	}
	if got := e.scalar("SELECT used_all_micro FROM api_keys WHERE id = $1", uuid.MustParse(e.keyID)); got != 2*messagesCost {
		t.Fatalf("persisted used_all %d", got)
	}
	// Redis lost: the meter is rebuilt from PostgreSQL and request_logs.
	testutil.Must(t, e.rep.App.St.DropKeyMeter(context.Background(), uuid.MustParse(e.keyID)))
	lim = gjson.Parse(rawGet(t, e.user, "/keys/"+e.keyID+"/limits"))
	if lim.Get("quota_used_micro").Int() != 2*messagesCost || lim.Get("windows.0.used_micro").Int() != 2*messagesCost {
		t.Fatalf("rebuilt limits %s", lim.Raw)
	}
}

func TestKeyQuotaScopeBalance(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, tiers: []string{"plan", "standard"}})
	e.patchKey(map[string]any{"quota_limit_micro": 3000, "quota_scope": "balance", "limit_5h_micro": 3000})
	// Plan charges do not count under the balance scope.
	for i := 0; i < 3; i++ {
		if resp, out := e.relayOpus(nil); resp.StatusCode != 200 {
			t.Fatalf("plan %d: %d %s", i, resp.StatusCode, out)
		}
	}
	lim := gjson.Parse(rawGet(t, e.user, "/keys/"+e.keyID+"/limits"))
	if lim.Get("quota_used_micro").Int() != 0 || lim.Get("used_all_micro").Int() != 3*messagesCost || lim.Get("windows.0.used_micro").Int() != 0 ||
		lim.Get("exhausted").Exists() {
		t.Fatalf("balance scope after plan %s", lim.Raw)
	}
}

// relayRejections returns the tier codes of a 429 tier rejection.
func relayRejections(out []byte) map[string]string {
	m := map[string]string{}
	for _, r := range gjson.GetBytes(out, "error.rejections").Array() {
		m[r.Get("tier").String()] = r.Get("code").String()
	}
	return m
}

// Balance scope, total quota used up: standard and force leave the tier
// chain with key_balance_quota_exhausted, plan is still tried.
func TestKeyBalanceQuotaKeepsPlan(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, tiers: []string{"standard", "plan"}})
	e.patchKey(map[string]any{"quota_limit_micro": 3000, "quota_scope": "balance"})
	// The first balance charge passes (remaining > 0, the last may overshoot).
	if resp, out := e.relayOpus(nil); resp.StatusCode != 200 || resp.Header.Get("X-Relay-Tier") != "standard" {
		t.Fatalf("standard: %d %s", resp.StatusCode, out)
	}
	// Exhausted: standard is skipped, plan admits.
	for i := 0; i < 2; i++ {
		if resp, out := e.relayOpus(nil); resp.StatusCode != 200 || resp.Header.Get("X-Relay-Tier") != "plan" {
			t.Fatalf("plan after balance quota %d: %d %s", i, resp.StatusCode, out)
		}
	}
	lim := gjson.Parse(rawGet(t, e.user, "/keys/"+e.keyID+"/limits"))
	if lim.Get("quota_used_micro").Int() != messagesCost || lim.Get("exhausted.code").String() != "key_balance_quota_exhausted" ||
		!lim.Get("exhausted.plan_available").Bool() || lim.Get("exhausted.window").Exists() {
		t.Fatalf("limits %s", lim.Raw)
	}
	// Only balance tiers left: the usual 429 with one entry per tier.
	e.patchKey(map[string]any{"tiers": []string{"standard", "force"}})
	resp, out := e.relayOpus(nil)
	rej := relayRejections(out)
	if resp.StatusCode != 429 || gjson.GetBytes(out, "error.type").String() != "rate_limit_error" ||
		rej["standard"] != "key_balance_quota_exhausted" || rej["force"] != "key_balance_quota_exhausted" || len(rej) != 2 ||
		!strings.Contains(gjson.GetBytes(out, "error.message").String(), "Plan 档仍可用") {
		t.Fatalf("balance tiers only: %d %s", resp.StatusCode, out)
	}
	e.persist()
	if n := e.scalar("SELECT count(*) FROM request_logs WHERE error_type = 'tier_rejected' AND status_code = 429"); n != 1 {
		t.Fatalf("tier_rejected logs %d", n)
	}
	if n := e.scalar("SELECT count(*) FROM request_logs WHERE error_type IN ('key_quota_exhausted', 'key_window_exhausted')"); n != 0 {
		t.Fatalf("whole-key rejections logged %d", n)
	}
}

// Plan used up too: the 429 lists the plan reason and the key's balance
// reason tier by tier.
func TestKeyBalanceQuotaAndPlanExhausted(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, planQuota: 3000, tiers: []string{"standard", "plan"}})
	e.patchKey(map[string]any{"quota_limit_micro": 3000, "quota_scope": "balance"})
	for _, want := range []string{"standard", "plan"} {
		if resp, out := e.relayOpus(nil); resp.StatusCode != 200 || resp.Header.Get("X-Relay-Tier") != want {
			t.Fatalf("%s: %d %s", want, resp.StatusCode, out)
		}
	}
	resp, out := e.relayOpus(nil)
	rej := relayRejections(out)
	if resp.StatusCode != 429 || rej["standard"] != "key_balance_quota_exhausted" || rej["plan"] != "plan_quota_exhausted" {
		t.Fatalf("both exhausted: %d %s", resp.StatusCode, out)
	}
	if m := gjson.GetBytes(out, "error.message").String(); !strings.Contains(m, "普通调用: key 余额口径") || !strings.Contains(m, "plan: ") {
		t.Fatalf("message %q", m)
	}
}

// Balance scope, a window used up: the same, with window and recovers_at.
func TestKeyBalanceWindowKeepsPlan(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, tiers: []string{"standard", "plan"}})
	e.patchKey(map[string]any{"quota_scope": "balance", "limit_5h_micro": 3000})
	if resp, out := e.relayOpus(nil); resp.StatusCode != 200 || resp.Header.Get("X-Relay-Tier") != "standard" {
		t.Fatalf("standard: %d %s", resp.StatusCode, out)
	}
	if resp, out := e.relayOpus(nil); resp.StatusCode != 200 || resp.Header.Get("X-Relay-Tier") != "plan" {
		t.Fatalf("plan after balance window: %d %s", resp.StatusCode, out)
	}
	e.patchKey(map[string]any{"tiers": []string{"force"}})
	resp, out := e.relayOpus(nil)
	r := gjson.GetBytes(out, "error.rejections.0")
	if resp.StatusCode != 429 || r.Get("tier").String() != "force" || r.Get("code").String() != "key_balance_quota_exhausted" || r.Get("window").String() != "5h" {
		t.Fatalf("force only: %d %s", resp.StatusCode, out)
	}
	rec, err := time.Parse(time.RFC3339, r.Get("recovers_at").String())
	if err != nil || rec.Before(time.Now().Add(4*time.Hour+58*time.Minute)) || rec.After(time.Now().Add(5*time.Hour+time.Minute)) {
		t.Fatalf("recovers_at %s %v", r.Get("recovers_at"), err)
	}
	lim := gjson.Parse(rawGet(t, e.user, "/keys/"+e.keyID+"/limits"))
	if lim.Get("exhausted.code").String() != "key_balance_quota_exhausted" || lim.Get("exhausted.window").String() != "5h" ||
		!lim.Get("exhausted.plan_available").Bool() || !lim.Get("exhausted.recovers_at").Exists() {
		t.Fatalf("limits %s", lim.Raw)
	}
}

// All scope keeps refusing the whole key for both the quota and a window;
// the status reports plan_available = false.
func TestKeyAllScopeRefusesWholeKey(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, tiers: []string{"standard", "plan"}})
	e.patchKey(map[string]any{"quota_scope": "all", "limit_5h_micro": 3000})
	if resp, out := e.relayOpus(nil); resp.StatusCode != 200 {
		t.Fatalf("first: %d %s", resp.StatusCode, out)
	}
	resp, out := e.relayOpus(nil)
	if resp.StatusCode != 429 || errCode(out) != "key_window_exhausted" || gjson.GetBytes(out, "error.rejections").Exists() {
		t.Fatalf("window: %d %s", resp.StatusCode, out)
	}
	lim := gjson.Parse(rawGet(t, e.user, "/keys/"+e.keyID+"/limits"))
	if lim.Get("exhausted.code").String() != "key_window_exhausted" || lim.Get("exhausted.plan_available").Bool() {
		t.Fatalf("limits %s", lim.Raw)
	}
	e.patchKey(map[string]any{"limit_5h_micro": nil, "quota_limit_micro": 3000})
	if resp, out := e.relayOpus(nil); resp.StatusCode != 429 || errCode(out) != "key_quota_exhausted" {
		t.Fatalf("quota: %d %s", resp.StatusCode, out)
	}
	code, body := publicQuery(t, e.rep.URL(), e.key)
	if code != 200 || gjson.Get(body, "limits.exhausted.code").String() != "key_quota_exhausted" || gjson.Get(body, "limits.exhausted.plan_available").Bool() {
		t.Fatalf("public %d %s", code, body)
	}
}

// keyHeldDuring starts a relay with a slow upstream and returns the key's
// held amount while it is in flight.
func keyHeldDuring(t *testing.T, e *env) (int64, string) {
	t.Helper()
	e.cpaA.Set(func(c *fakecpa.CPA) { c.Delay = 600 * time.Millisecond })
	defer e.cpaA.Set(func(c *fakecpa.CPA) { c.Delay = 0 })
	done := make(chan string, 1)
	go func() {
		resp, out := e.relayOpus(nil)
		done <- fmt.Sprintf("%d %s %s", resp.StatusCode, resp.Header.Get("X-Relay-Tier"), out)
	}()
	time.Sleep(300 * time.Millisecond)
	u, err := e.rep.App.Proxy.Meter.Usage(context.Background(), uuid.MustParse(e.keyID), time.Now())
	testutil.Must(t, err)
	return u.Held, <-done
}

// Holds: under the balance scope plan requests are not bounded by the key,
// standard / force holds are capped by the key's remaining quota and window.
func TestKeyBalanceHoldBounds(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, tiers: []string{"plan"}})
	e.patchKey(map[string]any{"quota_scope": "balance", "quota_limit_micro": 2000, "limit_1d_micro": 1500})
	if held, res := keyHeldDuring(t, e); held != 0 || !strings.HasPrefix(res, "200 plan") {
		t.Fatalf("plan hold on key %d (%s)", held, res)
	}
	e.patchKey(map[string]any{"tiers": []string{"standard"}})
	if held, res := keyHeldDuring(t, e); held != 1500 || !strings.HasPrefix(res, "200 standard") {
		t.Fatalf("standard hold on key %d, want 1500 (%s)", held, res)
	}
	// The all scope bounds plan holds too.
	e.patchKey(map[string]any{"tiers": []string{"plan"}, "quota_scope": "all", "limit_1d_micro": nil, "quota_limit_micro": 1_000_000})
	e.persist()
	lim := gjson.Parse(rawGet(t, e.user, "/keys/"+e.keyID+"/limits"))
	want := lim.Get("quota_remaining_micro").Int()
	if want <= 0 {
		t.Fatalf("limits %s", lim.Raw)
	}
	e.patchKey(map[string]any{"quota_limit_micro": lim.Get("quota_used_micro").Int() + 1000})
	if held, res := keyHeldDuring(t, e); held != 1000 || !strings.HasPrefix(res, "200 plan") {
		t.Fatalf("all-scope plan hold on key %d, want 1000 (%s)", held, res)
	}
}

func TestKeyWindowAndReset(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true})
	e.patchKey(map[string]any{"limit_5h_micro": 3000})
	if resp, out := e.relayOpus(nil); resp.StatusCode != 200 {
		t.Fatalf("first: %d %s", resp.StatusCode, out)
	}
	resp, out := e.relayOpus(nil)
	if resp.StatusCode != 429 || errCode(out) != "key_window_exhausted" || gjson.GetBytes(out, "error.window").String() != "5h" {
		t.Fatalf("window: %d %s", resp.StatusCode, out)
	}
	rec, err := time.Parse(time.RFC3339, gjson.GetBytes(out, "error.recovers_at").String())
	if err != nil || rec.Before(time.Now().Add(4*time.Hour+58*time.Minute)) || rec.After(time.Now().Add(5*time.Hour+time.Minute)) {
		t.Fatalf("recovers_at %s %v", gjson.GetBytes(out, "error.recovers_at"), err)
	}
	// Total quota: reset brings it back to 0, the sliding window is unaffected.
	e.patchKey(map[string]any{"limit_5h_micro": nil, "quota_limit_micro": 3000})
	if resp, out := e.relayOpus(nil); resp.StatusCode != 429 || errCode(out) != "key_quota_exhausted" {
		t.Fatalf("quota: %d %s", resp.StatusCode, out)
	}
	e.user.must("POST", "/keys/"+e.keyID+"/reset-usage", nil, 200)
	e.persist()
	e.waitOutbox()
	lim := gjson.Parse(rawGet(t, e.user, "/keys/"+e.keyID+"/limits"))
	if lim.Get("quota_used_micro").Int() != 0 || lim.Get("windows.0.used_micro").Int() != messagesCost || !lim.Get("usage_reset_at").Exists() {
		t.Fatalf("after reset %s", lim.Raw)
	}
	if resp, out := e.relayOpus(nil); resp.StatusCode != 200 {
		t.Fatalf("after reset: %d %s", resp.StatusCode, out)
	}
	// The admin can reset too (audited).
	e.admin.must("POST", "/admin/keys/"+e.keyID+"/reset-usage", nil, 200)
	if n := e.scalar("SELECT count(*) FROM audit_logs WHERE action = 'key.reset_usage'"); n != 1 {
		t.Fatalf("audit %d", n)
	}
}

func TestKeyStaticRejections(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true})
	// Model allowlist (normalized) and the filtered model list.
	e.patchKey(map[string]any{"model_allowlist": []string{"Claude-Sonnet-4.6"}})
	if resp, out := e.relayOpus(nil); resp.StatusCode != 403 || errCode(out) != "model_not_allowed" ||
		gjson.GetBytes(out, "error.type").String() != "permission_error" {
		t.Fatalf("model: %d %s", resp.StatusCode, out)
	}
	req, _ := http.NewRequest("GET", e.rep.URL()+"/v1/models", nil)
	req.Header.Set("x-api-key", e.key)
	resp, err := http.DefaultClient.Do(req)
	testutil.Must(t, err)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if ids := gjson.GetBytes(raw, "data.#.id").Array(); len(ids) != 1 || ids[0].String() != "claude-sonnet-4.6" {
		t.Fatalf("models %s", raw)
	}
	// IP allowlist (the test client is 127.0.0.1).
	e.patchKey(map[string]any{"model_allowlist": nil, "ip_allowlist": []string{"10.0.0.0/8"}})
	if resp, out := e.relayOpus(nil); resp.StatusCode != 403 || errCode(out) != "ip_not_allowed" {
		t.Fatalf("ip: %d %s", resp.StatusCode, out)
	}
	k := e.patchKey(map[string]any{"ip_allowlist": []string{"127.0.0.1", "10.1.2.3/8"}})
	if fmt.Sprint(k["ip_allowlist"]) != "[127.0.0.1/32 10.0.0.0/8]" {
		t.Fatalf("normalized ips %v", k["ip_allowlist"])
	}
	if resp, out := e.relayOpus(nil); resp.StatusCode != 200 {
		t.Fatalf("ip allowed: %d %s", resp.StatusCode, out)
	}
	if code := e.user.do("PATCH", "/keys/"+e.keyID, map[string]any{"ip_allowlist": []string{"not-an-ip"}}, nil); code != 400 {
		t.Fatalf("bad ip accepted: %d", code)
	}
	// Expiry.
	k = e.patchKey(map[string]any{"expires_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)})
	if k["status"] != "expired" {
		t.Fatalf("status %v", k["status"])
	}
	if resp, out := e.relayOpus(nil); resp.StatusCode != 403 || errCode(out) != "key_expired" {
		t.Fatalf("expired: %d %s", resp.StatusCode, out)
	}
	e.persist()
	for _, code := range []string{"model_not_allowed", "ip_not_allowed", "key_expired"} {
		if n := e.scalar("SELECT count(*) FROM request_logs WHERE error_type = $1", code); n != 1 {
			t.Fatalf("%s logs %d", code, n)
		}
	}
}

// The hold is capped by the key's remaining quota and windows like a
// balance: positive remaining admits, the last request may overshoot.
func TestKeyHoldCappedByRemaining(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true})
	ctx := context.Background()
	st := e.rep.App.St
	key := uuid.New()
	w := rstate.UserWallet(e.userID)
	now := time.Now()
	testutil.Must(t, st.SyncBase(ctx, w, 1_000_000, 1))
	testutil.Must(t, st.SyncKeyBase(ctx, key, 700, 0, 1))
	testutil.Must(t, st.RebuildKeyWindows(ctx, key, nil, now))
	kh := &rstate.KeyHold{Key: key, Limits: rstate.KeyLimits{Scope: "a", Quota: 1000, Windows: [3]int64{-1, 250, -1}}}
	// Quota remaining 300, 1d window remaining 250: held 250.
	res, err := st.HoldWithKey(ctx, w, kh, "r1", 10_000, time.Minute, now)
	testutil.Must(t, err)
	if !res.OK || res.Held != 250 {
		t.Fatalf("first hold %+v", res)
	}
	// Window remaining 250 − 250 held = 0: refused by the 1d window.
	res, err = st.HoldWithKey(ctx, w, kh, "r2", 10, time.Minute, now)
	testutil.Must(t, err)
	if res.OK || res.Reason != rstate.HoldKeyWindow1d {
		t.Fatalf("second hold %+v", res)
	}
	testutil.Must(t, st.ReleaseKeyHold(ctx, key, "r1"))
	kh.Limits.Windows[1] = -1
	res, err = st.HoldWithKey(ctx, w, kh, "r3", 10_000, time.Minute, now)
	testutil.Must(t, err)
	if !res.OK || res.Held != 300 {
		t.Fatalf("quota-capped hold %+v", res)
	}
	res, err = st.HoldWithKey(ctx, w, kh, "r4", 10, time.Minute, now)
	testutil.Must(t, err)
	if res.OK || res.Reason != rstate.HoldKeyQuota {
		t.Fatalf("quota refusal %+v", res)
	}
}

// Window sums slide minute by minute: a bucket leaves exactly span minutes
// after its minute.
func TestKeyWindowBoundary(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1, noPlan: true})
	ctx := context.Background()
	st := e.rep.App.St
	key := uuid.New()
	now := time.Unix(time.Now().Unix()/60*60+30, 0) // mid-minute
	nowMin := now.Unix() / 60
	testutil.Must(t, st.SyncKeyBase(ctx, key, 0, 0, 1))
	testutil.Must(t, st.RebuildKeyWindows(ctx, key, []rstate.MinuteAmount{
		{Minute: nowMin - 300, All: 1000, Balance: 1000}, // just outside 5h
		{Minute: nowMin - 299, All: 200},                 // oldest minute inside 5h
		{Minute: nowMin - 1439, All: 30, Balance: 30},    // inside 1d
		{Minute: nowMin - 1440, All: 4},                  // just outside 1d
	}, now))
	u, err := st.KeyUsage(ctx, key, now)
	testutil.Must(t, err)
	if u.WindowAll != [3]int64{200, 1200 + 30, 1234} || u.WindowBalance != [3]int64{0, 1030, 1030} {
		t.Fatalf("windows all %v balance %v", u.WindowAll, u.WindowBalance)
	}
	// One minute later the 200 bucket leaves the 5h window.
	u, err = st.KeyUsage(ctx, key, now.Add(time.Minute))
	testutil.Must(t, err)
	if u.WindowAll[0] != 0 {
		t.Fatalf("5h after a minute %d", u.WindowAll[0])
	}
	// Recovery: limit 150 over buckets 100@m0, 100@m0+10 recovers when m0 leaves.
	at, ok := rstate.RecoveryTime([]rstate.MinuteAmount{{Minute: 1000, All: 100}, {Minute: 1010, All: 100}}, 150, 300)
	if !ok || at.Unix() != (1000+300)*60 {
		t.Fatalf("recovery %v %v", at, ok)
	}
	if _, ok := rstate.RecoveryTime([]rstate.MinuteAmount{{Minute: 1, All: 10}}, 150, 300); ok {
		t.Fatal("not exhausted but recovery reported")
	}
}

func publicQuery(t *testing.T, base, key string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/v1/public/key-usage", nil)
	if key != "" {
		req.Header.Set("x-api-key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	testutil.Must(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestPublicKeyUsage(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, tiers: []string{"plan", "standard"}})
	e.patchKey(map[string]any{"quota_limit_micro": 1_000_000, "name": "laptop"})
	if resp, _ := e.relayOpus(nil); resp.StatusCode != 200 {
		t.Fatal("relay")
	}
	e.persist()
	code, body := publicQuery(t, e.rep.URL(), e.key)
	if code != 200 {
		t.Fatalf("query %d %s", code, body)
	}
	if gjson.Get(body, "account").Exists() {
		t.Fatalf("account figures without allow_owner_query: %s", body)
	}
	if gjson.Get(body, "key.name").String() != "laptop" || gjson.Get(body, "key.status").String() != "active" ||
		gjson.Get(body, "limits.quota_used_micro").Int() != messagesCost || len(gjson.Get(body, "recent").Array()) != 1 {
		t.Fatalf("body %s", body)
	}
	if strings.Contains(body, "upstream_cost") || strings.Contains(body, e.key) {
		t.Fatalf("leaks upstream cost or the key: %s", body)
	}
	// Bearer works too; with the owner query enabled the account figures appear.
	e.patchKey(map[string]any{"allow_owner_query": true})
	req, _ := http.NewRequest("GET", e.rep.URL()+"/api/v1/public/key-usage", nil)
	req.Header.Set("Authorization", "Bearer "+e.key)
	resp, err := http.DefaultClient.Do(req)
	testutil.Must(t, err)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !gjson.GetBytes(raw, "account.balance_available_micro").Exists() || len(gjson.GetBytes(raw, "account.plans").Array()) != 1 {
		t.Fatalf("owner query %d %s", resp.StatusCode, raw)
	}
	// Disabled keys still answer with their status; unknown keys are 401.
	e.patchKey(map[string]any{"enabled": false})
	if code, body := publicQuery(t, e.rep.URL(), e.key); code != 200 || gjson.Get(body, "key.status").String() != "disabled" {
		t.Fatalf("disabled %d %s", code, body)
	}
	if code, _ := publicQuery(t, e.rep.URL(), "sk-unknown"); code != 401 {
		t.Fatalf("unknown key %d", code)
	}
	// The key in the URL is ignored.
	if code, _ := publicQuery(t, e.rep.URL()+"/api/v1/public/key-usage?key="+e.key+"&x=", ""); code != 401 && code != 404 {
		t.Fatalf("query string key accepted: %d", code)
	}
	// Rate limit per key (30 / minute).
	limited := false
	for i := 0; i < 32; i++ {
		if code, _ := publicQuery(t, e.rep.URL(), e.key); code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("public query not rate limited")
	}
}

func TestBridgeStatusNotBilled(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true})
	e.patchKey(map[string]any{"limit_1d_micro": 500_000, "allow_owner_query": true})
	ctx := context.Background()
	before, _ := e.rep.App.St.R.XLen(ctx, e.rep.App.St.StreamKey()).Result()
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest("GET", e.rep.URL()+"/v1/bridge/status", nil)
		req.Header.Set("Authorization", "Bearer "+e.key)
		resp, err := http.DefaultClient.Do(req)
		testutil.Must(t, err)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || gjson.GetBytes(raw, "limits.windows.1.limit_micro").Int() != 500_000 ||
			gjson.GetBytes(raw, "account.balance_available_micro").Int() != 1_000_000 || gjson.GetBytes(raw, "recent").Exists() {
			t.Fatalf("status %d %s", resp.StatusCode, raw)
		}
	}
	after, _ := e.rep.App.St.R.XLen(ctx, e.rep.App.St.StreamKey()).Result()
	e.persist()
	if after != before || e.scalar("SELECT count(*) FROM request_logs") != 0 ||
		e.scalar("SELECT balance_micro FROM users WHERE id = $1", e.userID) != 1_000_000 {
		t.Fatalf("status was billed or logged: stream %d→%d", before, after)
	}
	req, _ := http.NewRequest("GET", e.rep.URL()+"/v1/bridge/status", nil)
	req.Header.Set("Authorization", "Bearer nope")
	resp, err := http.DefaultClient.Do(req)
	testutil.Must(t, err)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("bad key %d", resp.StatusCode)
	}
}

// exportCodes returns the full codes of a batch from the CSV export.
func exportCodes(t *testing.T, c *client, batch string) []string {
	t.Helper()
	req, _ := http.NewRequest("GET", c.base+"/api/v1/admin/redeem-batches/"+batch+"/export", nil)
	resp, err := c.http.Do(req)
	testutil.Must(t, err)
	defer resp.Body.Close()
	rows, err := csv.NewReader(resp.Body).ReadAll()
	testutil.Must(t, err)
	var out []string
	for _, r := range rows[1:] {
		out = append(out, r[0])
	}
	return out
}

func TestRedeemCodes(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 0, noPlan: true})
	b := e.admin.must("POST", "/admin/redeem-batches", map[string]any{"amount_micro": 2_000_000, "count": 3, "prefix": "oxa", "note": "t"}, 201)
	batch := b["id"].(string)
	codes := exportCodes(t, e.admin, batch)
	if len(codes) != 3 || !strings.HasPrefix(codes[0], "OXA-") || len(codes[0]) != len("OXA-7K2M-Q9TD-4HXR-PN3C") {
		t.Fatalf("codes %v", codes)
	}
	// Concurrent redemption of one code by several users: exactly one wins.
	users := make([]*client, 6)
	for i := range users {
		name := fmt.Sprintf("r%d", i)
		e.admin.must("POST", "/admin/users", map[string]any{"username": name, "password": name + "-password"}, 201)
		users[i] = newClient(t, e.rep.URL())
		users[i].login(name, name+"-password")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, used := 0, 0
	for _, u := range users {
		wg.Add(1)
		go func(u *client) {
			defer wg.Done()
			var out json.RawMessage
			code := u.do("POST", "/balance/redeem", map[string]any{"code": strings.ToLower(codes[0])}, &out)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case code == 200:
				ok++
			case gjson.GetBytes(out, "error.code").String() == "redeem_code_used":
				used++
			default:
				t.Errorf("unexpected %d %s", code, out)
			}
		}(u)
	}
	wg.Wait()
	if ok != 1 || used != len(users)-1 {
		t.Fatalf("ok %d used %d", ok, used)
	}
	if n := e.scalar("SELECT count(*) FROM ledger_entries WHERE type = 'redeem'"); n != 1 {
		t.Fatalf("redeem ledger %d", n)
	}
	// alice redeems the second code (body only, dashes/spaces ignored): balance +2.
	body := strings.ReplaceAll(codes[1][4:], "-", " ")
	res := e.user.must("POST", "/balance/redeem", map[string]any{"code": body}, 200)
	if int64(res["amount_micro"].(float64)) != 2_000_000 || int64(res["balance_micro"].(float64)) != 2_000_000 {
		t.Fatalf("redeem %v", res)
	}
	e.waitOutbox()
	if bal := gjson.Get(rawGet(t, e.user, "/balance"), "available_micro").Int(); bal != 2_000_000 {
		t.Fatalf("available after redeem %d", bal)
	}
	// Voided and expired codes are refused.
	list := gjson.Parse(rawGet(t, e.admin, "/admin/redeem-codes?status=unused&batch_id="+batch))
	if n := len(list.Get("items").Array()); n != 1 {
		t.Fatalf("unused %d", n)
	}
	e.admin.must("POST", "/admin/redeem-codes/"+list.Get("items.0.id").String()+"/void", nil, 200)
	var out json.RawMessage
	if code := e.user.do("POST", "/balance/redeem", map[string]any{"code": codes[2]}, &out); code != 400 || errCode(out) != "redeem_code_void" {
		t.Fatalf("void %d %s", code, out)
	}
	b2 := e.admin.must("POST", "/admin/redeem-batches", map[string]any{"amount_micro": 1, "count": 1,
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, 201)
	exp := exportCodes(t, e.admin, b2["id"].(string))[0]
	_, err := e.rep.App.Pool.Exec(context.Background(), "UPDATE redeem_batches SET expires_at = now() - interval '1 minute' WHERE id = $1", uuid.MustParse(b2["id"].(string)))
	testutil.Must(t, err)
	if code := e.user.do("POST", "/balance/redeem", map[string]any{"code": exp}, &out); code != 400 || errCode(out) != "redeem_code_expired" {
		t.Fatalf("expired %d %s", code, out)
	}
	// Repeated failures lock redemption (2 failures so far + 3 invalid = 5).
	for i := 0; i < 3; i++ {
		e.user.do("POST", "/balance/redeem", map[string]any{"code": "AAAA-BBBB-CCCC-DDDD"}, &out)
	}
	if code := e.user.do("POST", "/balance/redeem", map[string]any{"code": codes[1]}, &out); code != 429 || errCode(out) != "redeem_locked" {
		t.Fatalf("lock %d %s", code, out)
	}
	if n := e.scalar("SELECT count(*) FROM audit_logs WHERE action IN ('redeem.create_batch', 'redeem.export', 'redeem.void')"); n < 5 {
		t.Fatalf("audit %d", n)
	}
}

func TestAnnouncements(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1, noPlan: true})
	now := time.Now().UTC()
	mk := func(title string, extra map[string]any) string {
		body := map[string]any{"title": title, "body": "line 1\nhttps://example.com", "level": "info", "enabled": true}
		for k, v := range extra {
			body[k] = v
		}
		return e.admin.must("POST", "/admin/announcements", body, 201)["id"].(string)
	}
	live := mk("live", map[string]any{"level": "warning", "starts_at": now.Add(-time.Hour).Format(time.RFC3339), "ends_at": now.Add(time.Hour).Format(time.RFC3339)})
	mk("future", map[string]any{"starts_at": now.Add(time.Hour).Format(time.RFC3339)})
	mk("ended", map[string]any{"ends_at": now.Add(-time.Minute).Format(time.RFC3339)})
	mk("off", map[string]any{"enabled": false})
	open := mk("open", nil)
	titles := func(c *client) string {
		return gjson.Get(rawGet(t, c, "/announcements"), "items.#.title").String()
	}
	if got := titles(e.user); got != `["live","open"]` {
		t.Fatalf("active %s", got)
	}
	e.user.must("POST", "/announcements/"+live+"/dismiss", nil, 204)
	if got := titles(e.user); got != `["open"]` {
		t.Fatalf("after dismiss %s", got)
	}
	if got := titles(e.admin); got != `["live","open"]` {
		t.Fatalf("other user %s", got)
	}
	e.admin.must("PATCH", "/admin/announcements/"+open, map[string]any{"title": "open", "body": "", "level": "info", "enabled": false}, 200)
	if got := titles(e.user); got != `[]` {
		t.Fatalf("after disable %s", got)
	}
	if code := e.admin.do("POST", "/admin/announcements", map[string]any{"title": "x", "body": "", "level": "info", "enabled": true,
		"starts_at": now.Format(time.RFC3339), "ends_at": now.Add(-time.Hour).Format(time.RFC3339)}, nil); code != 400 {
		t.Fatalf("bad window accepted %d", code)
	}
	e.admin.must("DELETE", "/admin/announcements/"+live, nil, 204)
}

func TestPricingAndAssignable(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1, noPlan: true})
	patch := map[string]any{"tier_multipliers": map[string]any{"plan": 1, "standard": 1.5, "force": 2}, "model_multipliers": map[string]any{"claude-opus-4-8": 2}}
	e.admin.must("PATCH", "/admin/settings", patch, 200)
	testutil.Eventually(t, 5*time.Second, func() bool { return e.rep.App.Settings.Get().TierMultipliers.Force == 2 }, "settings reloaded")
	p := gjson.Parse(rawGet(t, e.user, "/pricing"))
	opus := p.Get(`items.#(model=="claude-opus-4-8")`)
	// Opus input $5 / M × 1.5 × 2 = $15 / M; output $25 × 2 × 2 = $100.
	if opus.Get("standard.input").Int() != 15_000_000 || opus.Get("force.output").Int() != 100_000_000 || !opus.Get("priced").Bool() {
		t.Fatalf("opus %s", opus.Raw)
	}
	if p.Get(`items.#(model=="claude-sonnet-4.6").plan.input`).Int() != 3_000_000 {
		t.Fatalf("sonnet %s", p.Raw)
	}
	acc := gjson.Parse(rawGet(t, e.admin, "/admin/accounts"))
	ov := gjson.Parse(rawGet(t, e.admin, "/admin/overview"))
	n := 0
	for _, a := range acc.Get("items").Array() {
		if a.Get("runtime.assignable").Bool() {
			n++
		}
	}
	if !acc.Get("items.0.runtime.assignable").Exists() || ov.Get("accounts.assignable").Int() != int64(n) || acc.Get("summary.assignable").Int() != int64(n) {
		t.Fatalf("assignable list %d overview %s", n, ov.Get("accounts").Raw)
	}
}

// waitMinuteHead sleeps past the end of the current minute when it is close,
// so a fixed one-minute rate window does not roll over mid-test.
func waitMinuteHead() {
	if s := time.Now().Second(); s >= 50 {
		time.Sleep(time.Duration(61-s) * time.Second)
	}
}

func bridgeStatus(t *testing.T, base, key string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/v1/bridge/status", nil)
	req.Header.Set("x-api-key", key)
	resp, err := http.DefaultClient.Do(req)
	testutil.Must(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// The public endpoint rate limits are runtime settings: a change on one
// replica applies at once on every replica (pub/sub).
func TestPublicRateLimitsFromSettings(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true})
	other := testutil.Start(t, e.schema, server.Options{DisableJobs: true, DisableConsumer: true, InstanceID: "other"})
	got := e.admin.must("GET", "/admin/settings", nil, 200)
	if got["public_query_ip_per_minute"] != float64(30) || got["public_query_key_per_minute"] != float64(30) || got["status_key_per_minute"] != float64(60) {
		t.Fatalf("defaults %v", got)
	}
	for _, bad := range []map[string]any{{"public_query_ip_per_minute": 0}, {"public_query_key_per_minute": 10001}, {"status_key_per_minute": -1}} {
		e.admin.must("PATCH", "/admin/settings", bad, 400)
	}
	e.admin.must("PATCH", "/admin/settings", map[string]any{"public_query_ip_per_minute": 5, "public_query_key_per_minute": 3, "status_key_per_minute": 2}, 200)
	testutil.Eventually(t, 5*time.Second, func() bool {
		s := other.App.Settings.Get()
		return s.PublicQueryIPPerMinute == 5 && s.PublicQueryKeyPerMinute == 3 && s.StatusKeyPerMinute == 2
	}, "the other replica did not reload the settings")
	waitMinuteHead()
	// Per key: 3 pass, the 4th is limited (on the other replica: shared counters).
	for i := 1; i <= 4; i++ {
		base := e.rep.URL()
		if i%2 == 0 {
			base = other.URL()
		}
		code, body := publicQuery(t, base, e.key)
		if (i <= 3 && code != 200) || (i == 4 && code != 429) {
			t.Fatalf("public query %d: %d %s", i, code, body)
		}
	}
	// Per IP (counted before the key): the 5th hit passes the IP check, the 6th not.
	if code, _ := publicQuery(t, other.URL(), "sk-unknown"); code != 401 {
		t.Fatalf("5th from the IP: %d", code)
	}
	if code, _ := publicQuery(t, other.URL(), "sk-unknown"); code != 429 {
		t.Fatalf("6th from the IP: %d", code)
	}
	// Status line: 2 per key per minute, the message names the limit.
	for i := 1; i <= 3; i++ {
		code, body := bridgeStatus(t, other.URL(), e.key)
		if (i <= 2 && code != 200) || (i == 3 && (code != 429 || !strings.Contains(body, "每分钟最多 2 次"))) {
			t.Fatalf("status %d: %d %s", i, code, body)
		}
	}
	// Raising the limit applies to the running minute right away.
	e.admin.must("PATCH", "/admin/settings", map[string]any{"status_key_per_minute": 100}, 200)
	testutil.Eventually(t, 5*time.Second, func() bool { return other.App.Settings.Get().StatusKeyPerMinute == 100 }, "reload")
	if code, body := bridgeStatus(t, other.URL(), e.key); code != 200 {
		t.Fatalf("status after raising: %d %s", code, body)
	}
}
