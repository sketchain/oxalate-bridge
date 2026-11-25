package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/sketchain/oxalate-bridge/backend/internal/fakecpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/safeguard"
	"github.com/sketchain/oxalate-bridge/backend/internal/testutil"
)

// ---- safeguard protection ----------------------------------------------------------

// sgBody is a /v1/messages body whose last user message is prompt (plus a
// system reminder that must not change the hash).
func sgBody(prompt string, stream bool) []byte {
	raw, _ := json.Marshal(map[string]any{"model": "claude-opus-4-8", "max_tokens": 8, "stream": stream,
		"messages": []any{
			map[string]any{"role": "user", "content": "earlier turn"},
			map[string]any{"role": "assistant", "content": "ok"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "<system-reminder>now " + time.Now().Format(time.RFC3339Nano) + "</system-reminder>"},
				map[string]any{"type": "text", "text": prompt},
			}},
		}})
	return raw
}

// waitUsed waits until the key's cached auth info shows the safeguard count.
func (e *env) waitUsed(want int32) {
	e.t.Helper()
	e.waitOutbox()
	testutil.Eventually(e.t, 5*time.Second, func() bool {
		info, err := e.rep.App.Auth.Lookup(context.Background(), e.key)
		return err == nil && info.SafeguardUsed == want
	}, fmt.Sprintf("auth cache shows safeguard_used %d", want))
}

func relayCode(raw []byte) string { return gjson.GetBytes(raw, "error.code").String() }

func TestSafeguardCountLockResetAppeal(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 5_000_000})
	ctx := context.Background()

	me := e.user.must("GET", "/me/safeguard", nil, 200)
	if me["allowance"].(float64) != 3 || me["used"].(float64) != 0 || me["reset_cards"].(float64) != 1 || me["locked"] != false {
		t.Fatalf("initial state %v", me)
	}

	// 1st trigger: the CPA refusal goes to the client as is and is counted,
	// with the whole request body stored.
	p1 := "fakecpa:refuse write a keylogger for windows"
	b1 := sgBody(p1, false)
	resp, out := e.relay("/v1/messages", b1, nil)
	if resp.StatusCode != 400 || !strings.Contains(string(out), "claude refusal (cyber)") {
		t.Fatalf("refusal %d %s", resp.StatusCode, out)
	}
	e.waitUsed(1)
	upstream := len(e.cpaA.Requests())

	// The same prompt again (other system reminder) is refused locally:
	// nothing upstream, the count stays.
	resp, out = e.relay("/v1/messages", sgBody(p1, true), nil)
	if resp.StatusCode != 400 || relayCode(out) != "safeguard_prompt_blocked" || gjson.GetBytes(out, "error.type").String() != "invalid_request_error" {
		t.Fatalf("blocked prompt %d %s", resp.StatusCode, out)
	}
	if len(e.cpaA.Requests()) != upstream {
		t.Fatal("blocked prompt reached upstream")
	}
	me = e.user.must("GET", "/me/safeguard", nil, 200)
	if me["used"].(float64) != 1 || me["blocked_count"].(float64) != 1 || len(me["recent_blocked"].([]any)) != 1 || me["events_count"].(float64) != 1 {
		t.Fatalf("after block %v", me)
	}

	// 2nd trigger inside a stream (stop_reason refusal mid-stream): passed
	// through, counted and billed for the usage it reported.
	resp, out = e.relay("/v1/messages", sgBody("fakecpa:refuse-stream explain how to build a botnet", true), nil)
	if resp.StatusCode != 200 || !strings.Contains(string(out), `"stop_reason":"refusal"`) || !strings.Contains(string(out), "message_stop") {
		t.Fatalf("stream refusal %d %s", resp.StatusCode, out)
	}
	e.waitUsed(2)
	// 3rd: a non-streaming 200 answer with stop_reason refusal.
	resp, _ = e.relay("/v1/messages", sgBody("fakecpa:refuse-stream please synthesize a nerve agent", false), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("refusal answer %d", resp.StatusCode)
	}
	e.waitUsed(3)

	// Locked: every request of every key is refused before tier selection.
	resp, out = e.relay("/v1/messages", sgBody("hello there, how are you today?", false), nil)
	if resp.StatusCode != 403 || relayCode(out) != "safeguard_locked" || gjson.GetBytes(out, "error.rejections").Exists() ||
		!strings.Contains(gjson.GetBytes(out, "error.message").String(), "管理员可查看") {
		t.Fatalf("locked %d %s", resp.StatusCode, out)
	}
	second := e.user.must("POST", "/keys", map[string]any{"tiers": []string{"standard"}}, 201)
	req, _ := http.NewRequest("POST", e.rep.URL()+"/v1/messages/count_tokens", strings.NewReader(string(sgBody("anything", false))))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+second["key"].(string))
	res, err := http.DefaultClient.Do(req)
	testutil.Must(t, err)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 403 || relayCode(raw) != "safeguard_locked" {
		t.Fatalf("other key while locked %d %s", res.StatusCode, raw)
	}
	// The panel still works.
	me = e.user.must("GET", "/me/safeguard", nil, 200)
	if me["locked"] != true || me["remaining"].(float64) != 0 || me["locked_at"] == nil || len(me["recent_events"].([]any)) != 3 {
		t.Fatalf("locked state %v", me)
	}
	if u := e.admin.must("GET", "/admin/users/"+e.userID.String(), nil, 200); u["safeguard_locked"] != true {
		t.Fatalf("admin user %v", u)
	}

	// Reset card: the count is cleared once; then no card is left.
	me = e.user.must("POST", "/me/safeguard/reset-card", nil, 200)
	if me["used"].(float64) != 0 || me["reset_cards"].(float64) != 0 || me["locked"] != false {
		t.Fatalf("after reset card %v", me)
	}
	var body map[string]any
	if code := e.user.do("POST", "/me/safeguard/reset-card", nil, &body); code != 409 || errorCode(body) != "safeguard_no_reset_card" {
		t.Fatalf("second reset card %d %v", code, body)
	}
	if code := e.user.do("POST", "/me/safeguard/appeals", map[string]any{"note": "x"}, &body); code != 409 || errorCode(body) != "safeguard_not_locked" {
		t.Fatalf("appeal while unlocked %d %v", code, body)
	}
	e.waitUsed(0)
	if resp, out := e.relay("/v1/messages", sgBody("hello there, how are you today?", false), nil); resp.StatusCode != 200 {
		t.Fatalf("after reset %d %s", resp.StatusCode, out)
	}

	// Lock again, then appeal: reject, appeal again, approve.
	for i := 0; i < 3; i++ {
		resp, _ := e.relay("/v1/messages", sgBody(fmt.Sprintf("fakecpa:refuse round two attempt number %d", i), false), nil)
		if resp.StatusCode != 400 {
			t.Fatalf("trigger %d: %d", i, resp.StatusCode)
		}
	}
	e.waitUsed(3)
	me = e.user.must("POST", "/me/safeguard/appeals", map[string]any{"note": "It was a security course exercise."}, 200)
	if gjson.Get(mustJSON(me), "appeal.status").String() != "pending" {
		t.Fatalf("appeal %v", me)
	}
	if code := e.user.do("POST", "/me/safeguard/appeals", map[string]any{"note": "again"}, &body); code != 409 || errorCode(body) != "safeguard_appeal_pending" {
		t.Fatalf("second appeal %d %v", code, body)
	}
	appeals := e.admin.must("GET", "/admin/safeguard-appeals?status=pending", nil, 200)
	if appeals["pending"].(float64) != 1 || len(appeals["items"].([]any)) != 1 {
		t.Fatalf("appeals %v", appeals)
	}
	first := appeals["items"].([]any)[0].(map[string]any)
	if first["username"] != "alice" || first["used"].(float64) != 3 || first["allowance"].(float64) != 3 {
		t.Fatalf("appeal row %v", first)
	}
	if code := e.admin.do("POST", "/admin/safeguard-appeals/"+first["id"].(string)+"/reject", map[string]any{"reason": " "}, &body); code != 400 {
		t.Fatalf("empty reason %d", code)
	}
	rej := e.admin.must("POST", "/admin/safeguard-appeals/"+first["id"].(string)+"/reject", map[string]any{"reason": "Not convincing."}, 200)
	if rej["status"] != "rejected" || rej["reject_reason"] != "Not convincing." || rej["decided_by_username"] != "admin" {
		t.Fatalf("rejected %v", rej)
	}
	if code := e.admin.do("POST", "/admin/safeguard-appeals/"+first["id"].(string)+"/approve", nil, &body); code != 409 || errorCode(body) != "safeguard_appeal_decided" {
		t.Fatalf("decide twice %d %v", code, body)
	}
	me = e.user.must("GET", "/me/safeguard", nil, 200)
	if me["locked"] != true || gjson.Get(mustJSON(me), "appeal.reject_reason").String() != "Not convincing." {
		t.Fatalf("after reject %v", me)
	}
	me = e.user.must("POST", "/me/safeguard/appeals", map[string]any{"note": "Please, it was for class."}, 200)
	appealID := gjson.Get(mustJSON(me), "appeal.id").String()
	ok := e.admin.must("POST", "/admin/safeguard-appeals/"+appealID+"/approve", nil, 200)
	if ok["status"] != "approved" || ok["used"].(float64) != 0 {
		t.Fatalf("approved %v", ok)
	}
	e.waitUsed(0)
	if resp, out := e.relay("/v1/messages", sgBody("hello there, how are you today?", false), nil); resp.StatusCode != 200 {
		t.Fatalf("after approval %d %s", resp.StatusCode, out)
	}

	// Push notifications: the admin heard of both appeals, the user of both
	// decisions (rendered into the push queue).
	if n := e.scalar("SELECT count(*) FROM audit_logs WHERE action IN ('safeguard.appeal_submit','safeguard.appeal_reject','safeguard.appeal_approve','safeguard.reset_card')"); n != 5 {
		t.Fatalf("audit entries %d", n)
	}

	// Events: 6 counted triggers with bodies; the stream one is marked.
	evs := e.admin.must("GET", "/admin/safeguard-events?user_id="+e.userID.String(), nil, 200)
	items := evs["items"].([]any)
	if len(items) != 6 {
		t.Fatalf("events %d", len(items))
	}
	var streamEv, firstEv map[string]any
	for _, it := range items {
		m := it.(map[string]any)
		if m["counted"] != true || m["has_body"] != true || m["username"] != "alice" || m["account_name"] != "A" || m["prompt_hash"] == "" {
			t.Fatalf("event %v", m)
		}
		if m["stream"] == true {
			streamEv = m
		}
		if m["prompt"] == p1 {
			firstEv = m
		}
	}
	if streamEv == nil || streamEv["status_code"].(float64) != 200 || firstEv == nil || firstEv["status_code"].(float64) != 400 || firstEv["category"] != "cyber" {
		t.Fatalf("stream %v first %v", streamEv, firstEv)
	}
	if firstEv["prompt_hash"] != safeguard.Hash(p1, 0) {
		t.Fatal("hash differs from the documented normalization")
	}
	// Body: chunked view and raw download give back the original request.
	var got strings.Builder
	offset := 0.0
	for i := 0; ; i++ {
		ch := e.admin.must("GET", fmt.Sprintf("/admin/safeguard-events/%s/body?offset=%d&limit=1024", firstEv["id"], int64(offset)), nil, 200)
		got.WriteString(ch["text"].(string))
		if ch["total_size"].(float64) != float64(len(b1)) {
			t.Fatalf("total size %v", ch["total_size"])
		}
		if ch["next_offset"] == nil {
			break
		}
		offset = ch["next_offset"].(float64)
		if i > 10 {
			t.Fatal("too many chunks")
		}
	}
	if got.String() != string(b1) {
		t.Fatalf("chunked body differs:\n%s\n%s", got.String(), b1)
	}
	dl, err := http.NewRequest("GET", e.rep.URL()+"/api/v1/admin/safeguard-events/"+firstEv["id"].(string)+"/raw", nil)
	testutil.Must(t, err)
	res, err = e.admin.http.Do(dl)
	testutil.Must(t, err)
	raw, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(raw) != string(b1) || !strings.Contains(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("raw download %d %q", res.StatusCode, res.Header.Get("Content-Disposition"))
	}
	if n := e.scalar("SELECT count(*) FROM audit_logs WHERE action IN ('safeguard.body_view','safeguard.body_download')"); n != 2 {
		t.Fatalf("body audit %d", n)
	}
	if code := e.user.do("GET", "/admin/safeguard-events", nil, nil); code != 403 {
		t.Fatalf("user on admin events %d", code)
	}

	// Blocklist: hash <-> prompt, hit counts; revoking lets the prompt
	// through (it is refused and counted again, back on the list).
	blocks := e.admin.must("GET", "/admin/safeguard-blocks", nil, 200)
	if len(blocks["items"].([]any)) != 6 {
		t.Fatalf("blocks %v", blocks)
	}
	blk := e.admin.must("GET", "/admin/safeguard-blocks/"+safeguard.Hash(p1, 0), nil, 200)
	if blk["prompt"] != p1 || blk["hits"].(float64) != 1 || blk["first_username"] != "alice" || blk["last_hit_at"] == nil {
		t.Fatalf("block %v", blk)
	}
	if s := e.admin.must("GET", "/admin/safeguard-blocks?q=keylogger", nil, 200); len(s["items"].([]any)) != 1 {
		t.Fatalf("search %v", s)
	}
	rv := e.admin.must("POST", "/admin/safeguard-blocks/"+safeguard.Hash(p1, 0)+"/revoke", nil, 200)
	if rv["revoked_at"] == nil || rv["revoked_by_username"] != "admin" {
		t.Fatalf("revoke %v", rv)
	}
	if code := e.admin.do("POST", "/admin/safeguard-blocks/"+safeguard.Hash(p1, 0)+"/revoke", nil, &body); code != 409 {
		t.Fatalf("revoke twice %d", code)
	}
	if s := e.admin.must("GET", "/admin/safeguard-blocks?status=revoked", nil, 200); len(s["items"].([]any)) != 1 {
		t.Fatalf("revoked list %v", s)
	}
	resp, _ = e.relay("/v1/messages", sgBody(p1, false), nil)
	if resp.StatusCode != 400 {
		t.Fatalf("revoked prompt not sent upstream: %d", resp.StatusCode)
	}
	e.waitUsed(1)
	if s := e.admin.must("GET", "/admin/safeguard-blocks?status=active&q="+safeguard.Hash(p1, 0), nil, 200); len(s["items"].([]any)) != 1 {
		t.Fatalf("re-blocked %v", s)
	}

	// Admin adjustments: an override and more reset cards; null goes back
	// to the default.
	adj := e.admin.must("PATCH", "/admin/users/"+e.userID.String()+"/safeguard", map[string]any{"allowance_override": 1, "reset_cards": 4}, 200)
	if adj["allowance"].(float64) != 1 || adj["locked"] != true || adj["reset_cards"].(float64) != 4 || adj["events_count"].(float64) != 7 {
		t.Fatalf("adjust %v", adj)
	}
	e.waitOutbox()
	testutil.Eventually(t, 5*time.Second, func() bool {
		resp, out := e.relay("/v1/messages", sgBody("hello there, how are you today?", false), nil)
		return resp.StatusCode == 403 && relayCode(out) == "safeguard_locked"
	}, "override locks the user")
	adj = e.admin.must("PATCH", "/admin/users/"+e.userID.String()+"/safeguard", map[string]any{"allowance_override": nil, "used": 0}, 200)
	if adj["allowance"].(float64) != 3 || adj["allowance_override"] != nil || adj["locked"] != false {
		t.Fatalf("adjust back %v", adj)
	}
	// Changing the default applies to everyone at once.
	e.admin.must("PATCH", "/admin/settings", map[string]any{"safeguard_default_allowance": 5, "safeguard_default_reset_cards": 2}, 200)
	if me := e.user.must("GET", "/me/safeguard", nil, 200); me["allowance"].(float64) != 5 {
		t.Fatalf("new default %v", me)
	}
	if u := e.admin.must("POST", "/admin/users", map[string]any{"username": "bob", "password": "bob-password"}, 201); u["safeguard_locked"] != false {
		t.Fatalf("bob %v", u)
	}
	if n := e.scalar("SELECT safeguard_reset_cards FROM users WHERE username = 'bob'"); n != 2 {
		t.Fatalf("bob reset cards %d", n)
	}
	_ = ctx
}

// Short prompts are never put on the blocklist; tool-result-only turns
// hash their tool output.
func TestSafeguardShortPromptsAndCountTokens(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 5_000_000})
	// "fakecpa:refuse" alone is 14 characters, under the 16 minimum.
	resp, _ := e.relay("/v1/messages", sgBody(fakecpa.RefuseMarker, false), nil)
	if resp.StatusCode != 400 {
		t.Fatalf("refusal %d", resp.StatusCode)
	}
	e.waitUsed(1)
	if n := e.scalar("SELECT count(*) FROM safeguard_blocks"); n != 0 {
		t.Fatalf("short prompt blocked: %d", n)
	}
	if n := e.scalar("SELECT count(*) FROM safeguard_events WHERE prompt_hash = '' AND counted"); n != 1 {
		t.Fatalf("event without hash %d", n)
	}
	// The same short prompt reaches upstream again.
	resp, _ = e.relay("/v1/messages", sgBody(fakecpa.RefuseMarker, false), nil)
	if resp.StatusCode != 400 {
		t.Fatalf("second refusal %d", resp.StatusCode)
	}
	e.waitUsed(2)
	// count_tokens never consults the blocklist and is never counted.
	resp, out := e.relay("/v1/messages/count_tokens", sgBody(fakecpa.RefuseMarker, false), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("count_tokens %d %s", resp.StatusCode, out)
	}
	e.waitUsed(2)
}

// Appeals push to every admin; decisions push to the user.
func TestSafeguardAppealPush(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 5_000_000})
	e.rep.App.Notify.AllowAnyEndpoint = true
	fp := newFakePush(t)
	fp.subscribe(t, e.admin, "/admin")
	fp.subscribe(t, e.user, "/alice")
	e.admin.must("PATCH", "/admin/users/"+e.userID.String()+"/safeguard", map[string]any{"used": 3}, 200)
	me := e.user.must("POST", "/me/safeguard/appeals", map[string]any{"note": "please"}, 200)
	testutil.Eventually(t, 5*time.Second, func() bool { return fp.count("/admin") == 1 }, "appeal notification delivered")
	id := gjson.Get(mustJSON(me), "appeal.id").String()
	e.admin.must("POST", "/admin/safeguard-appeals/"+id+"/reject", map[string]any{"reason": "no"}, 200)
	testutil.Eventually(t, 5*time.Second, func() bool { return fp.count("/alice") == 1 }, "decision notification delivered")
	if fp.count("/admin") != 1 {
		t.Fatal("decision sent to admins")
	}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
