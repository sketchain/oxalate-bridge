package e2e

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"

	"github.com/sketchain/oxalate-bridge/backend/internal/fakecpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/jobs"
	"github.com/sketchain/oxalate-bridge/backend/internal/testutil"
)

// Cost of the fake's default usage (100 in, 50 out, 1000 cache read, 200
// cache write) on claude-opus-4-8 ($5 / $25 / $0.5 / $6.25 per M).
const messagesCost = 3500

// Chat / Responses report cache inside the prompt and no cache writes:
// 100 uncached, 1000 cached, 50 out.
const openaiCost = 2250

func TestPassthroughHeadersAndLedger(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true})
	body := []byte("{\"model\":  \"claude-opus-4-8\", \"max_tokens\":64,\n  \"stream\": true,\n \"messages\":[{\"role\":\"user\",\"content\":\"h\\u00e9llo\"}],\"metadata\":{\"user_id\":\"x\"}}")
	resp, out := e.relay("/v1/messages", body, map[string]string{
		"User-Agent": "claude-cli/2.0 (external, cli)", "anthropic-version": "2023-06-01", "anthropic-beta": "prompt-caching-2024-07-31",
		"X-Claude-Code-Session-Id": "sess-1", "Cookie": "a=b", "X-Forwarded-For": "1.2.3.4", "X-Goog-Api-Key": "leak",
		"X-Forwarded-Proto": "https",
	})
	if resp.StatusCode != 200 || resp.Header.Get("X-Relay-Tier") != "standard" || resp.Header.Get("X-Relay-Request-Id") == "" {
		t.Fatalf("status %d tier %q id %q: %s", resp.StatusCode, resp.Header.Get("X-Relay-Tier"), resp.Header.Get("X-Relay-Request-Id"), out)
	}
	if !bytes.Contains(out, []byte("message_stop")) {
		t.Fatalf("stream not relayed: %s", out)
	}
	reqs := e.cpaA.Requests()
	if len(reqs) != 1 {
		t.Fatalf("upstream requests %d", len(reqs))
	}
	got := reqs[0]
	if !bytes.Equal(got.Body, body) {
		t.Fatalf("body changed:\n%s\n%s", got.Body, body)
	}
	h := got.Header
	if h.Get("Authorization") != "Bearer "+e.cpaA.Key || h.Get("User-Agent") != "claude-cli/2.0 (external, cli)" ||
		h.Get("Anthropic-Version") != "2023-06-01" || h.Get("Anthropic-Beta") == "" || h.Get("X-Claude-Code-Session-Id") != "sess-1" ||
		h.Get("Accept-Encoding") != "identity" {
		t.Fatalf("headers not forwarded: %v", h)
	}
	for _, name := range []string{"Cookie", "X-Forwarded-For", "X-Forwarded-Proto", "X-Goog-Api-Key", "X-Api-Key"} {
		if h.Get(name) != "" {
			t.Fatalf("header %s leaked upstream", name)
		}
	}

	// Responses (non-stream) is forwarded byte for byte too.
	rbody := []byte(`{"model":"claude-opus-4-8","input":"hi",  "prompt_cache_key":"pk-1"}`)
	resp, out = e.relay("/v1/responses", rbody, nil)
	if resp.StatusCode != 200 || !bytes.Equal(e.cpaA.Requests()[1].Body, rbody) {
		t.Fatalf("responses: %d %s", resp.StatusCode, out)
	}

	// Chat stream without include_usage: inserted upstream, usage chunk hidden.
	cbody := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	resp, out = e.relay("/v1/chat/completions", cbody, nil)
	up := e.cpaA.Requests()[2].Body
	if resp.StatusCode != 200 || !gjson.GetBytes(up, "stream_options.include_usage").Bool() {
		t.Fatalf("include_usage not inserted: %s", up)
	}
	if bytes.Contains(out, []byte(`"usage"`)) || !bytes.Contains(out, []byte("[DONE]")) {
		t.Fatalf("usage chunk leaked or stream cut: %s", out)
	}
	// count_tokens is logged but not billed.
	resp, _ = e.relay("/v1/messages/count_tokens", []byte(`{"model":"claude-opus-4-8","messages":[]}`), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("count_tokens %d", resp.StatusCode)
	}

	e.persist()
	total := int64(messagesCost + 2*openaiCost)
	if got := e.scalar("SELECT COALESCE(sum(billed_micro),0) FROM request_logs"); got != total {
		t.Fatalf("billed %d want %d", got, total)
	}
	if got := e.scalar("SELECT count(*) FROM request_logs"); got != 4 {
		t.Fatalf("logs %d", got)
	}
	if got := e.scalar("SELECT billed_micro FROM request_logs WHERE endpoint = '/v1/messages/count_tokens'"); got != 0 {
		t.Fatalf("count_tokens billed %d", got)
	}
	if got := e.scalar("SELECT input_tokens + cache_read_tokens FROM request_logs WHERE endpoint = '/v1/chat/completions'"); got != 1100 {
		t.Fatalf("chat tokens %d", got)
	}
	if got := e.scalar("SELECT balance_micro FROM users WHERE id = $1", e.userID); got != 1_000_000-total {
		t.Fatalf("balance %d", got)
	}
	if got := e.scalar("SELECT -sum(amount_micro) FROM ledger_entries WHERE type = 'api_charge'"); got != total {
		t.Fatalf("api_charge %d", got)
	}
	if got := e.scalar("SELECT sum(billed_micro) FROM usage_hourly"); got != total {
		t.Fatalf("hourly billed %d", got)
	}
	if got := e.scalar("SELECT sum(requests) FROM usage_hourly"); got != 4 {
		t.Fatalf("hourly requests %d", got)
	}
	// Real-time balance equals PG once nothing is pending.
	var bal map[string]any
	e.user.do("GET", "/balance", nil, &bal)
	if int64(bal["available_micro"].(float64)) != 1_000_000-total || int64(bal["pending_micro"].(float64)) != 0 {
		t.Fatalf("balance figures %v", bal)
	}
	// Summary API reads the rollup.
	from := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	to := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	var sum map[string]any
	if code := e.user.do("GET", "/usage/summary?granularity=day&from="+from+"&to="+to, nil, &sum); code != 200 {
		t.Fatalf("summary %d %v", code, sum)
	}
	var billed float64
	for _, it := range sum["items"].([]any) {
		billed += it.(map[string]any)["billed_micro"].(float64)
	}
	if int64(billed) != total {
		t.Fatalf("summary billed %v", billed)
	}
	res, err := jobs.Reconcile(context.Background(), e.rep.App.Store, testutil.Logger())
	if err != nil || !res.OK {
		t.Fatalf("reconcile %+v %v", res, err)
	}
}

func TestTierOrderAndRejections(t *testing.T) {
	// Plan quota below one request: the first request is admitted (quota
	// positive) and overdraws the period; the next falls to standard.
	e := setup(t, envOpts{balanceMicro: 5000, planQuota: 3000})
	body := []byte(`{"model":"claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	resp, out := e.relay("/v1/messages", body, map[string]string{"X-Claude-Code-Session-Id": "s"})
	if resp.StatusCode != 200 || resp.Header.Get("X-Relay-Tier") != "plan" {
		t.Fatalf("first: %d %s %s", resp.StatusCode, resp.Header.Get("X-Relay-Tier"), out)
	}
	resp, out = e.relay("/v1/messages", body, map[string]string{"X-Claude-Code-Session-Id": "s"})
	if resp.StatusCode != 200 || resp.Header.Get("X-Relay-Tier") != "standard" {
		t.Fatalf("second: %d %s %s", resp.StatusCode, resp.Header.Get("X-Relay-Tier"), out)
	}
	// Balance 5000 − 3500 = 1500 > 0: still admitted, then overdrawn.
	resp, _ = e.relay("/v1/messages", body, nil)
	if resp.StatusCode != 200 || resp.Header.Get("X-Relay-Tier") != "standard" {
		t.Fatalf("third: %d", resp.StatusCode)
	}
	// Both tiers now refuse; the error lists each reason.
	resp, out = e.relay("/v1/messages", body, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("fourth: %d %s", resp.StatusCode, out)
	}
	msg := gjson.GetBytes(out, "error.message").String()
	if !strings.Contains(msg, "plan 本期额度已用完") || !strings.Contains(msg, "余额不足") {
		t.Fatalf("rejection message %q", msg)
	}
	if n := len(gjson.GetBytes(out, "error.rejections").Array()); n != 2 {
		t.Fatalf("rejections %s", out)
	}
	e.persist()
	if got := e.scalar("SELECT used_billed_micro FROM plan_usage_periods WHERE subscription_id = $1", uuid.MustParse(e.subID)); got != messagesCost {
		t.Fatalf("plan used %d", got)
	}
	if got := e.scalar("SELECT count(*) FROM request_logs WHERE tier = 'plan'"); got != 1 {
		t.Fatalf("plan logs %d", got)
	}
	if got := e.scalar("SELECT count(*) FROM request_logs WHERE status_code = 429 AND error_type = 'tier_rejected'"); got != 1 {
		t.Fatalf("rejected logs %d", got)
	}
	if got := e.scalar("SELECT balance_micro FROM users WHERE id = $1", e.userID); got != 5000-2*messagesCost {
		t.Fatalf("balance %d", got)
	}
	// Plan ledger is separate from balance: no api_charge for the plan request.
	if got := e.scalar("SELECT -sum(amount_micro) FROM ledger_entries WHERE type = 'api_charge'"); got != 2*messagesCost {
		t.Fatalf("api_charge %d", got)
	}
	res, err := jobs.Reconcile(context.Background(), e.rep.App.Store, testutil.Logger())
	if err != nil || !res.OK {
		t.Fatalf("reconcile %+v %v", res, err)
	}
	// My subscriptions shows the period usage.
	var subs map[string]any
	e.user.do("GET", "/subscriptions", nil, &subs)
	cp := subs["items"].([]any)[0].(map[string]any)["current_period"].(map[string]any)
	if int64(cp["used_billed_micro"].(float64)) != messagesCost || int64(cp["remaining_micro"].(float64)) != 3000-messagesCost {
		t.Fatalf("current period %v", cp)
	}
}

func TestModelNotInPlanFallsThrough(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, allowed: []string{"Claude-Sonnet-4.6"}})
	resp, _ := e.relay("/v1/messages", []byte(`{"model":"claude-opus-4-8","messages":[]}`), nil)
	if resp.StatusCode != 200 || resp.Header.Get("X-Relay-Tier") != "standard" {
		t.Fatalf("opus: %d %s", resp.StatusCode, resp.Header.Get("X-Relay-Tier"))
	}
	resp, _ = e.relay("/v1/messages", []byte(`{"model":"anthropic/claude-sonnet-4.6(high)","messages":[]}`), nil)
	if resp.StatusCode != 200 || resp.Header.Get("X-Relay-Tier") != "plan" {
		t.Fatalf("sonnet: %d %s", resp.StatusCode, resp.Header.Get("X-Relay-Tier"))
	}
}

func TestStickySession(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true, secondAcct: true})
	i := 0
	e.rep.App.Router.Rand = func() float64 { i++; return float64(i%10) / 10 }
	body := []byte(`{"model":"claude-opus-4-8","messages":[]}`)
	for n := 0; n < 8; n++ {
		resp, out := e.relay("/v1/messages", body, map[string]string{"X-Claude-Code-Session-Id": "sticky-1"})
		if resp.StatusCode != 200 {
			t.Fatalf("%d %s", resp.StatusCode, out)
		}
	}
	a, b := len(e.cpaA.Requests()), len(e.cpaB.Requests())
	if (a != 8 || b != 0) && (a != 0 || b != 8) {
		t.Fatalf("session split across accounts: A=%d B=%d", a, b)
	}
	// Without a session id every request is its own (key) session; the key
	// id is the fallback, so it is sticky per key as well.
	for n := 0; n < 4; n++ {
		e.relay("/v1/messages", body, nil)
	}
	a2, b2 := len(e.cpaA.Requests())-a, len(e.cpaB.Requests())-b
	if (a2 != 4 || b2 != 0) && (a2 != 0 || b2 != 4) {
		t.Fatalf("key fallback session split: A=%d B=%d", a2, b2)
	}
}

func TestRetryCooldownAndAccountError(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true, secondAcct: true})
	app := e.rep.App
	app.Router.Rand = func() float64 { return 0 } // always the first candidate (A)
	ctx := context.Background()
	accA, accB := uuid.MustParse(e.accA), uuid.MustParse(e.accB)
	body := []byte(`{"model":"claude-opus-4-8","messages":[]}`)

	// 5xx on A: retried on B, A cools down 30 s.
	e.cpaA.Push(fakecpa.Reply{Status: 502, Body: `{"error":"bad gateway"}`})
	resp, out := e.relay("/v1/messages", body, map[string]string{"X-Session-ID": "r1"})
	if resp.StatusCode != 200 || len(e.cpaB.Requests()) != 1 {
		t.Fatalf("5xx retry: %d %s", resp.StatusCode, out)
	}
	cd, ok := app.St.Cooldown(ctx, accA)
	if !ok || time.Until(cd.Until) < 20*time.Second || time.Until(cd.Until) > 31*time.Second {
		t.Fatalf("5xx cooldown %+v %v", cd, ok)
	}
	// While A cools down, new sessions go to B.
	e.relay("/v1/messages", body, map[string]string{"X-Session-ID": "r2"})
	if len(e.cpaA.Requests()) != 1 || len(e.cpaB.Requests()) != 2 {
		t.Fatalf("cooldown ignored: A=%d B=%d", len(e.cpaA.Requests()), len(e.cpaB.Requests()))
	}

	// 429 with Retry-After on A: cooldown follows it.
	testutil.Must(t, app.St.ClearCooldown(ctx, accA))
	e.cpaA.Push(fakecpa.Reply{Status: 429, Body: `{"error":"rate"}`, Header: map[string]string{"Retry-After": "120"}})
	resp, _ = e.relay("/v1/messages", body, map[string]string{"X-Session-ID": "r3"})
	if resp.StatusCode != 200 {
		t.Fatalf("429 retry %d", resp.StatusCode)
	}
	cd, _ = app.St.Cooldown(ctx, accA)
	if time.Until(cd.Until) < 100*time.Second {
		t.Fatalf("429 cooldown %v", cd.Until)
	}

	// 401 on A: account marked error and no longer routed.
	testutil.Must(t, app.St.ClearCooldown(ctx, accA))
	e.cpaA.Push(fakecpa.Reply{Status: 401, Body: `{"error":"revoked"}`})
	resp, _ = e.relay("/v1/messages", body, map[string]string{"X-Session-ID": "r4"})
	if resp.StatusCode != 200 {
		t.Fatalf("401 retry %d", resp.StatusCode)
	}
	if e.scalar("SELECT count(*) FROM accounts WHERE id = $1 AND error_status", accA) != 1 {
		t.Fatal("account not marked error")
	}
	testutil.Must(t, app.St.ClearCooldown(ctx, accA))
	testutil.Must(t, app.Cat.ReloadDB(ctx))
	before := len(e.cpaA.Requests())
	e.relay("/v1/messages", body, map[string]string{"X-Session-ID": "r5"})
	if len(e.cpaA.Requests()) != before {
		t.Fatal("errored account still routed")
	}

	// Both accounts failing: the last upstream error reaches the client.
	testutil.Must(t, app.Store.Q.ClearAccountError(ctx, accA))
	testutil.Must(t, app.Cat.ReloadDB(ctx))
	e.cpaA.Push(fakecpa.Reply{Status: 503, Body: `{"error":"a down"}`})
	e.cpaB.Push(fakecpa.Reply{Status: 503, Body: `{"error":"b down"}`})
	resp, out = e.relay("/v1/messages", body, map[string]string{"X-Session-ID": "r6"})
	if resp.StatusCode != 503 || !bytes.Contains(out, []byte("b down")) {
		t.Fatalf("exhausted retries: %d %s", resp.StatusCode, out)
	}
	_ = accB

	e.persist()
	if e.scalar("SELECT count(*) FROM request_logs WHERE retries = 1 AND status_code = 200") != 3 {
		t.Fatal("retries not logged")
	}
	if e.scalar("SELECT billed_micro FROM request_logs WHERE status_code = 503") != 0 {
		t.Fatal("failed request billed")
	}
}

func TestPlanTierDoesNotRetry(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 0, secondAcct: true, tiers: []string{"plan"}})
	e.cpaA.Push(fakecpa.Reply{Status: 500, Body: `{"error":"boom"}`})
	resp, out := e.relay("/v1/messages", []byte(`{"model":"claude-opus-4-8","messages":[]}`), nil)
	if resp.StatusCode != 500 || len(e.cpaB.Requests()) != 0 {
		t.Fatalf("plan retried: %d %s B=%d", resp.StatusCode, out, len(e.cpaB.Requests()))
	}
}

func TestRefusalPassthroughAndSafeguard(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true, secondAcct: true})
	e.rep.App.Router.Rand = func() float64 { return 0 }
	refusal := fakecpa.RefusalBody("cyber", "malware request")
	e.cpaA.Push(fakecpa.Reply{Status: 400, Body: refusal})
	resp, out := e.relay("/v1/messages", []byte(`{"model":"claude-opus-4-8","messages":[]}`), nil)
	if resp.StatusCode != 400 || string(out) != refusal || len(e.cpaB.Requests()) != 0 {
		t.Fatalf("refusal: %d %s (B=%d)", resp.StatusCode, out, len(e.cpaB.Requests()))
	}
	e.persist()
	var cat, reason string
	testutil.Must(t, e.rep.App.Pool.QueryRow(context.Background(), "SELECT category, reason FROM safeguard_events").Scan(&cat, &reason))
	if cat != "cyber" || reason != "malware request" {
		t.Fatalf("safeguard %q %q", cat, reason)
	}
	var page map[string]any
	e.admin.do("GET", "/admin/safeguard-events", nil, &page)
	if len(page["items"].([]any)) != 1 {
		t.Fatalf("admin safeguard list %v", page)
	}
}

func TestUsageMissingBillsInputEstimate(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, noPlan: true})
	e.cpaA.Push(fakecpa.Reply{Status: 200, Body: `{"id":"msg","content":[]}`})
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"` + strings.Repeat("x", 2990) + `"}]}`)
	resp, _ := e.relay("/v1/messages", body, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	e.persist()
	// len(body)/3 tokens at $5/M.
	want := int64(float64(len(body)) / 3 / 1e6 * 5 * 1e6)
	got := e.scalar("SELECT billed_micro FROM request_logs WHERE usage_missing")
	if got < want-1 || got > want+1 {
		t.Fatalf("usage-missing charge %d want ~%d", got, want)
	}
}
