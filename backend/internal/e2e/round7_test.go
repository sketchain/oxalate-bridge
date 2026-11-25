package e2e

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sketchain/oxalate-bridge/backend/internal/notify"
	"github.com/sketchain/oxalate-bridge/backend/internal/server"
	"github.com/sketchain/oxalate-bridge/backend/internal/testutil"
)

// ---- admin API tokens -----------------------------------------------------------

// tokenReq calls the panel API with an admin token and no CSRF header.
func tokenReq(t *testing.T, base, token, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = strings.NewReader(string(raw))
	}
	req, _ := http.NewRequest(method, base+"/api/v1"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func apiCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func createToken(t *testing.T, admin *client, body map[string]any) (string, string) {
	t.Helper()
	out := admin.must("POST", "/admin/admin-tokens", body, 201)
	secret := out["secret"].(string)
	if !strings.HasPrefix(secret, "oba-") || len(secret) < 44 {
		t.Fatalf("secret %q", secret)
	}
	return secret, out["token"].(map[string]any)["id"].(string)
}

func TestAdminTokenScopes(t *testing.T) {
	schema := testutil.Schema(t)
	rep := testutil.Start(t, schema, server.Options{DisableJobs: true, DisableConsumer: true})
	admin := newClient(t, rep.URL())
	admin.login("admin", "admin-password")
	u := admin.must("POST", "/admin/users", map[string]any{"username": "bob", "password": "bob-password"}, 201)
	bobID := u["id"].(string)
	var adminID string
	{
		me := admin.must("GET", "/me", nil, 200)
		adminID = me["id"].(string)
	}

	read, readID := createToken(t, admin, map[string]any{"name": "grafana", "scope": "read"})
	write, writeID := createToken(t, admin, map[string]any{"name": "ops-bot", "scope": "write"})
	base := rep.URL()

	// The secret is shown once: the list has only the prefix.
	list := admin.must("GET", "/admin/admin-tokens", nil, 200)
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), read) || strings.Contains(string(raw), write) || strings.Contains(string(raw), "secret") {
		t.Fatalf("token list leaks the secret: %s", raw)
	}
	if n := len(list["items"].([]any)); n != 2 {
		t.Fatalf("tokens %d", n)
	}

	// read: GET / HEAD only, SSE included.
	if st, _ := tokenReq(t, base, read, "GET", "/admin/users", nil); st != 200 {
		t.Fatalf("read GET users %d", st)
	}
	if st, m := tokenReq(t, base, read, "POST", "/admin/users", map[string]any{"username": "x1", "password": "x1-password"}); st != 403 || apiCode(m) != "admin_token_read_only" {
		t.Fatalf("read POST %d %v", st, m)
	}
	if st, m := tokenReq(t, base, read, "PATCH", "/admin/settings", map[string]any{"max_retries": 3}); st != 403 || apiCode(m) != "admin_token_read_only" {
		t.Fatalf("read PATCH settings %d %v", st, m)
	}
	{
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/admin/events", nil)
		req.Header.Set("Authorization", "Bearer "+read)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("read SSE %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		_, _ = bufio.NewReader(resp.Body).ReadByte()
		cancel()
		resp.Body.Close()
	}

	// write: every admin operation, no CSRF header needed.
	if st, m := tokenReq(t, base, write, "POST", "/admin/users", map[string]any{"username": "carol", "password": "carol-password"}); st != 201 {
		t.Fatalf("write POST user %d %v", st, m)
	}
	if st, m := tokenReq(t, base, write, "PATCH", "/admin/users/"+bobID, map[string]any{"note": "via token", "password": "new-bob-password"}); st != 200 {
		t.Fatalf("write PATCH user %d %v", st, m)
	}

	// Forbidden for both scopes: privilege escalation and token management.
	for _, tok := range []string{read, write} {
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/admin/admin-tokens", nil},
			{"POST", "/admin/admin-tokens", map[string]any{"name": "x", "scope": "write"}},
			{"POST", "/admin/admin-tokens/" + readID + "/revoke", nil},
		} {
			if st, m := tokenReq(t, base, tok, c.method, c.path, c.body); st != 403 || apiCode(m) != "admin_token_forbidden" {
				t.Fatalf("%s %s with token: %d %v", c.method, c.path, st, m)
			}
		}
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", "/admin/users/" + bobID, map[string]any{"role": "admin"}},
		{"PATCH", "/admin/users/" + adminID, map[string]any{"password": "hijacked-password"}},
		{"POST", "/admin/users", map[string]any{"username": "eve", "password": "eve-password", "role": "admin"}},
		{"PATCH", "/admin/users/" + adminID, map[string]any{"status": "disabled"}},
		{"DELETE", "/admin/users/" + adminID, nil},
	} {
		if st, m := tokenReq(t, base, write, c.method, c.path, c.body); st != 403 || apiCode(m) != "admin_token_forbidden" {
			t.Fatalf("%s %s with write token: %d %v", c.method, c.path, st, m)
		}
	}
	// A token does not open the user endpoints.
	if st, _ := tokenReq(t, base, write, "GET", "/me", nil); st != 401 {
		t.Fatalf("token on /me: %d", st)
	}
	// The session still manages tokens and roles.
	admin.must("PATCH", "/admin/users/"+bobID, map[string]any{"role": "admin"}, 200)
	// Other admins cannot be disabled or deleted with a token either.
	for _, c := range []struct {
		method string
		body   any
	}{{"PATCH", map[string]any{"status": "disabled"}}, {"DELETE", nil}} {
		if st, m := tokenReq(t, base, write, c.method, "/admin/users/"+bobID, c.body); st != 403 || apiCode(m) != "admin_token_forbidden" {
			t.Fatalf("%s other admin with write token: %d %v", c.method, st, m)
		}
	}

	// Audit: token id and name, operator = creator.
	logs := admin.must("GET", "/admin/audit-logs?target_type=user", nil, 200)
	found := false
	for _, it := range logs["items"].([]any) {
		e := it.(map[string]any)
		if e["action"] == "user.create" && e["token_id"] == writeID {
			found = true
			if e["token_name"] != "ops-bot" || e["operator_id"] != adminID {
				t.Fatalf("audit entry %v", e)
			}
		}
	}
	if !found {
		t.Fatalf("no audit entry with the token: %v", logs)
	}
	// Last use is recorded.
	for _, it := range admin.must("GET", "/admin/admin-tokens", nil, 200)["items"].([]any) {
		tk := it.(map[string]any)
		if tk["last_used_at"] == nil || tk["last_used_ip"] == "" {
			t.Fatalf("last use not recorded: %v", tk)
		}
	}
}

func TestAdminTokenExpiryRevokeIPRate(t *testing.T) {
	schema := testutil.Schema(t)
	rep := testutil.Start(t, schema, server.Options{DisableJobs: true, DisableConsumer: true})
	admin := newClient(t, rep.URL())
	admin.login("admin", "admin-password")
	base := rep.URL()
	ctx := context.Background()

	if st, m := tokenReq(t, base, "oba-doesnotexist0000000000000000000000000000000", "GET", "/admin/users", nil); st != 401 || apiCode(m) != "admin_token_invalid" {
		t.Fatalf("unknown token %d %v", st, m)
	}
	// Past expiry is refused at creation.
	admin.must("POST", "/admin/admin-tokens", map[string]any{"name": "old", "scope": "read",
		"expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}, 400)

	exp, expID := createToken(t, admin, map[string]any{"name": "exp", "scope": "read",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if st, _ := tokenReq(t, base, exp, "GET", "/admin/users", nil); st != 200 {
		t.Fatalf("before expiry %d", st)
	}
	_, err := rep.App.Pool.Exec(ctx, "UPDATE admin_tokens SET expires_at = now() - interval '1 second' WHERE id = $1", expID)
	testutil.Must(t, err)
	if st, m := tokenReq(t, base, exp, "GET", "/admin/users", nil); st != 401 || apiCode(m) != "admin_token_expired" {
		t.Fatalf("expired %d %v", st, m)
	}

	rev, revID := createToken(t, admin, map[string]any{"name": "rev", "scope": "write"})
	admin.must("POST", "/admin/admin-tokens/"+revID+"/revoke", nil, 200)
	admin.must("POST", "/admin/admin-tokens/"+revID+"/revoke", nil, 409)
	if st, m := tokenReq(t, base, rev, "GET", "/admin/users", nil); st != 401 || apiCode(m) != "admin_token_revoked" {
		t.Fatalf("revoked %d %v", st, m)
	}
	for _, it := range admin.must("GET", "/admin/admin-tokens", nil, 200)["items"].([]any) {
		tk := it.(map[string]any)
		want := map[string]string{expID: "expired", revID: "revoked"}[tk["id"].(string)]
		if want != "" && tk["status"] != want {
			t.Fatalf("status %v want %s", tk, want)
		}
	}

	// IP allowlist (the test client is 127.0.0.1).
	admin.must("POST", "/admin/admin-tokens", map[string]any{"name": "bad-ip", "scope": "read", "ip_allowlist": []string{"not-an-ip"}}, 400)
	denied, _ := createToken(t, admin, map[string]any{"name": "lan", "scope": "read", "ip_allowlist": []string{"10.0.0.0/8"}})
	if st, m := tokenReq(t, base, denied, "GET", "/admin/users", nil); st != 403 || apiCode(m) != "admin_token_ip_denied" {
		t.Fatalf("ip denied %d %v", st, m)
	}
	allowed, _ := createToken(t, admin, map[string]any{"name": "local", "scope": "read", "ip_allowlist": []string{"127.0.0.1", "::1"}})
	if st, _ := tokenReq(t, base, allowed, "GET", "/admin/users", nil); st != 200 {
		t.Fatalf("ip allowed %d", st)
	}

	// Rate limit per token (fixed one-minute windows: 7 calls at a limit of
	// 3 must hit it even across a minute boundary).
	admin.must("PATCH", "/admin/settings", map[string]any{"admin_token_per_minute": 3}, 200)
	limited, _ := createToken(t, admin, map[string]any{"name": "rl", "scope": "read"})
	other, _ := createToken(t, admin, map[string]any{"name": "rl2", "scope": "read"})
	got429 := false
	for i := range 7 {
		st, m := tokenReq(t, base, limited, "GET", "/admin/users", nil)
		if i == 0 && st != 200 {
			t.Fatalf("first call %d", st)
		}
		if st == 429 {
			if apiCode(m) != "admin_token_rate_limited" {
				t.Fatalf("429 code %v", m)
			}
			got429 = true
		}
	}
	if !got429 {
		t.Fatal("rate limit never hit")
	}
	// Another token has its own budget.
	if st, _ := tokenReq(t, base, other, "GET", "/admin/users", nil); st != 200 {
		t.Fatalf("other token %d", st)
	}
}

// ---- Web Push -----------------------------------------------------------------------

// fakePush is a push service: it records deliveries per endpoint path and
// answers with the status set for that path (default 201).
type fakePush struct {
	srv    *httptest.Server
	mu     sync.Mutex
	hits   map[string]int
	status map[string]int
	urg    map[string]string
}

func newFakePush(t *testing.T) *fakePush {
	f := &fakePush{hits: map[string]int{}, status: map[string]int{}, urg: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("TTL") != "3600" || r.Header.Get("Content-Encoding") != "aes128gcm" ||
			!strings.HasPrefix(r.Header.Get("Authorization"), "vapid ") {
			w.WriteHeader(400)
			return
		}
		f.hits[r.URL.Path]++
		f.urg[r.URL.Path] = r.Header.Get("Urgency")
		st := f.status[r.URL.Path]
		if st == 0 {
			st = 201
		}
		w.WriteHeader(st)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePush) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *fakePush) setStatus(path string, st int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[path] = st
}

// subscribe registers a browser-like subscription for c at path.
func (f *fakePush) subscribe(t *testing.T, c *client, path string) string {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	testutil.Must(t, err)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	out := c.must("POST", "/push/subscriptions", map[string]any{"endpoint": f.srv.URL + path,
		"keys": map[string]any{"p256dh": base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
			"auth": base64.RawURLEncoding.EncodeToString(auth)}}, 201)
	return out["id"].(string)
}

func startPush(t *testing.T) (*testutil.Replica, string) {
	t.Helper()
	schema := testutil.Schema(t)
	rep := testutil.Start(t, schema, server.Options{DisableJobs: true, DisableConsumer: true, AllowAnyPushEndpoint: true})
	return rep, schema
}

func TestPushDevicesAndTest(t *testing.T) {
	rep, _ := startPush(t)
	fp := newFakePush(t)
	admin := newClient(t, rep.URL())
	admin.login("admin", "admin-password")

	cfg := admin.must("GET", "/push/config", nil, 200)
	if k, _ := cfg["public_key"].(string); len(k) < 80 {
		t.Fatalf("vapid public key %q", k)
	}
	// The same key after a restart of the replica (stored in settings).
	if k := rep.App.Notify.PublicKey(); k != cfg["public_key"] {
		t.Fatal("public key mismatch")
	}

	// Device label from the User-Agent.
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	req, _ := http.NewRequest("POST", rep.URL()+"/api/v1/push/subscriptions", strings.NewReader(`{"endpoint":"`+fp.srv.URL+`/iphone","keys":{"p256dh":"`+
		base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())+`","auth":"AAAAAAAAAAAAAAAAAAAAAA"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "test")
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1")
	resp, err := admin.http.Do(req)
	testutil.Must(t, err)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("subscribe %d", resp.StatusCode)
	}
	desk := fp.subscribe(t, admin, "/desktop")
	devices := admin.must("GET", "/push/subscriptions", nil, 200)["items"].([]any)
	if len(devices) != 2 {
		t.Fatalf("devices %v", devices)
	}
	labels := map[string]bool{}
	for _, d := range devices {
		labels[d.(map[string]any)["device_label"].(string)] = true
	}
	if !labels["iPhone · Safari"] {
		t.Fatalf("labels %v", labels)
	}

	// Test notification reaches every device.
	out := admin.must("POST", "/push/test", nil, 200)
	if out["devices"].(float64) != 2 {
		t.Fatalf("test %v", out)
	}
	testutil.Eventually(t, 5*time.Second, func() bool { return fp.count("/iphone") == 1 && fp.count("/desktop") == 1 }, "test delivered")

	// Another user cannot delete my device.
	admin.must("POST", "/admin/users", map[string]any{"username": "mallory", "password": "mallory-password"}, 201)
	m := newClient(t, rep.URL())
	m.login("mallory", "mallory-password")
	m.must("DELETE", "/push/subscriptions/"+desk, nil, 404)
	admin.must("DELETE", "/push/subscriptions/"+desk, nil, 204)
	if n := len(admin.must("GET", "/push/subscriptions", nil, 200)["items"].([]any)); n != 1 {
		t.Fatalf("after delete %d", n)
	}

	// Unknown hosts are refused unless the test option allows them.
	rep.App.Notify.AllowAnyEndpoint = false
	admin.must("POST", "/push/subscriptions", map[string]any{"endpoint": "https://evil.example/push",
		"keys": map[string]any{"p256dh": "x", "auth": "y"}}, 400)
	rep.App.Notify.AllowAnyEndpoint = true

	// Locale is stored for push texts.
	admin.must("PUT", "/me/locale", map[string]any{"locale": "en"}, 204)
	if me := admin.must("GET", "/me", nil, 200); me["locale"] != "en" {
		t.Fatalf("locale %v", me["locale"])
	}
	admin.must("PUT", "/me/locale", map[string]any{"locale": "fr"}, 400)
}

func TestPushExpiredSubscriptionsRemoved(t *testing.T) {
	rep, _ := startPush(t)
	fp := newFakePush(t)
	admin := newClient(t, rep.URL())
	admin.login("admin", "admin-password")
	fp.subscribe(t, admin, "/gone")
	fp.subscribe(t, admin, "/missing")
	fp.subscribe(t, admin, "/flaky")
	fp.setStatus("/gone", 410)
	fp.setStatus("/missing", 404)
	fp.setStatus("/flaky", 500)
	admin.must("POST", "/push/test", nil, 200)
	testutil.Eventually(t, 5*time.Second, func() bool {
		return len(admin.must("GET", "/push/subscriptions", nil, 200)["items"].([]any)) == 1
	}, "410 / 404 subscriptions removed")
	// The 5xx one stays, counts a failure and is retried later.
	items := admin.must("GET", "/push/subscriptions", nil, 200)["items"].([]any)
	d := items[0].(map[string]any)
	if !strings.HasSuffix(d["endpoint"].(string), "/flaky") {
		t.Fatalf("left %v", d)
	}
	testutil.Eventually(t, 5*time.Second, func() bool {
		return admin.must("GET", "/push/subscriptions", nil, 200)["items"].([]any)[0].(map[string]any)["failures"].(float64) >= 1
	}, "failure counted")
	if rep.App.Notify.QueueLen(context.Background()) != 1 {
		t.Fatal("5xx delivery not requeued")
	}
}

func TestNotificationPrefsByRole(t *testing.T) {
	rep, _ := startPush(t)
	admin := newClient(t, rep.URL())
	admin.login("admin", "admin-password")
	admin.must("POST", "/admin/users", map[string]any{"username": "dave", "password": "dave-password"}, 201)
	user := newClient(t, rep.URL())
	user.login("dave", "dave-password")

	defaults := map[string]bool{"account_auth_error": true, "account_stale": true, "subscription_pending": true,
		"reconcile_mismatch": true, "stream_backlog_high": true, "account_full": false, "safeguard_event": false,
		"balance_low": true, "plan_expiring": true, "auto_renew_failed": true, "key_exhausted": false,
		"safeguard_appeal_new": true, "safeguard_appeal_result": true}
	check := func(c *client, wantAdmin bool) map[string]any {
		p := c.must("GET", "/notifications/prefs", nil, 200)
		if p["balance_threshold_micro"].(float64) != 5_000_000 {
			t.Fatalf("threshold %v", p["balance_threshold_micro"])
		}
		n := 0
		for _, it := range p["items"].([]any) {
			e := it.(map[string]any)
			ev := e["event"].(string)
			if e["group"] == "admin" {
				if !wantAdmin {
					t.Fatalf("user sees admin event %s", ev)
				}
			}
			if e["enabled"] != defaults[ev] || e["default_enabled"] != defaults[ev] {
				t.Fatalf("%s: %v", ev, e)
			}
			n++
		}
		want := 5
		if wantAdmin {
			want = 13
		}
		if n != want {
			t.Fatalf("events %d want %d", n, want)
		}
		return p
	}
	check(admin, true)
	check(user, false)
	user.must("PUT", "/notifications/prefs", map[string]any{"items": []any{map[string]any{"event": "account_stale", "enabled": false}}}, 403)
	p := user.must("PUT", "/notifications/prefs", map[string]any{"items": []any{map[string]any{"event": "key_exhausted", "enabled": true}},
		"balance_threshold_micro": 12_500_000}, 200)
	if p["balance_threshold_micro"].(float64) != 12_500_000 {
		t.Fatalf("threshold %v", p)
	}
	for _, it := range p["items"].([]any) {
		e := it.(map[string]any)
		if e["event"] == "key_exhausted" && e["enabled"] != true {
			t.Fatalf("key_exhausted %v", e)
		}
	}
	user.must("PUT", "/notifications/prefs", map[string]any{"balance_threshold_micro": -1}, 400)
}

// queued returns the deliveries queued since the last call.
type queueCounter struct {
	svc  *notify.Service
	last int64
}

func (q *queueCounter) delta() int64 {
	n := q.svc.Queued()
	d := n - q.last
	q.last = n
	return d
}

func TestNotifyStateChangesAndDedup(t *testing.T) {
	rep, schema := startPush(t)
	fp := newFakePush(t)
	ctx := context.Background()
	admin := newClient(t, rep.URL())
	admin.login("admin", "admin-password")
	fp.subscribe(t, admin, "/admin")
	svc := rep.App.Notify
	q := &queueCounter{svc: svc}
	acct := uuid.New()

	obs := func(auth, stale bool, full string) {
		t.Helper()
		testutil.Must(t, svc.ObserveAccounts(ctx, []notify.AccountObs{{ID: acct, Name: "A", AuthError: auth, Reason: "401",
			Stale: stale, Age: 10 * time.Minute, Full: full}}))
	}
	obs(false, false, "")
	if d := q.delta(); d != 0 {
		t.Fatalf("healthy account notified %d", d)
	}
	obs(true, false, "")
	if d := q.delta(); d != 1 {
		t.Fatalf("auth error: %d", d)
	}
	testutil.Eventually(t, 5*time.Second, func() bool { return fp.count("/admin") == 1 }, "delivered")
	fp.mu.Lock()
	if fp.urg["/admin"] != "high" {
		t.Fatalf("urgency %q", fp.urg["/admin"])
	}
	fp.mu.Unlock()
	// Still in error on the next polls: nothing new.
	obs(true, false, "")
	obs(true, false, "")
	if d := q.delta(); d != 0 {
		t.Fatalf("repeated poll notified %d", d)
	}
	// Recovers and fails again within 30 minutes: deduplicated.
	obs(false, false, "")
	obs(true, false, "")
	if d := q.delta(); d != 0 {
		t.Fatalf("dedup window ignored: %d", d)
	}
	// Stale fires once; account_full is off by default.
	obs(true, true, "5h")
	if d := q.delta(); d != 1 {
		t.Fatalf("stale: %d", d)
	}
	obs(false, false, "")
	admin.must("PUT", "/notifications/prefs", map[string]any{"items": []any{map[string]any{"event": "account_full", "enabled": true}}}, 200)
	obs(false, false, "week")
	if d := q.delta(); d != 1 {
		t.Fatalf("account_full after enabling: %d", d)
	}

	// Backlog: only after 5 minutes above the threshold, once.
	t0 := time.Now().UTC()
	now := t0
	svc.Now = func() time.Time { return now }
	defer func() { svc.Now = nil }()
	testutil.Must(t, svc.ObserveBacklog(ctx, 5000, 1000))
	now = t0.Add(4 * time.Minute)
	testutil.Must(t, svc.ObserveBacklog(ctx, 5000, 1000))
	if d := q.delta(); d != 0 {
		t.Fatalf("backlog before 5 min: %d", d)
	}
	now = t0.Add(5*time.Minute + time.Second)
	testutil.Must(t, svc.ObserveBacklog(ctx, 5000, 1000))
	now = t0.Add(7 * time.Minute)
	testutil.Must(t, svc.ObserveBacklog(ctx, 5000, 1000))
	if d := q.delta(); d != 1 {
		t.Fatalf("backlog: %d", d)
	}
	// A dip resets the clock.
	testutil.Must(t, svc.ObserveBacklog(ctx, 10, 1000))
	now = t0.Add(8 * time.Minute)
	testutil.Must(t, svc.ObserveBacklog(ctx, 5000, 1000))
	if d := q.delta(); d != 0 {
		t.Fatalf("backlog after dip: %d", d)
	}
	svc.Now = nil

	// Reconcile: on the transition to a mismatch only.
	testutil.Must(t, svc.ObserveReconcile(ctx, true, 0, 0))
	testutil.Must(t, svc.ObserveReconcile(ctx, false, 2, 1500))
	testutil.Must(t, svc.ObserveReconcile(ctx, false, 2, 1500))
	if d := q.delta(); d != 1 {
		t.Fatalf("reconcile: %d", d)
	}

	// Dedup across replicas: two replicas announce the same event at once.
	rep2 := testutil.Start(t, schema, server.Options{DisableJobs: true, DisableConsumer: true, AllowAnyPushEndpoint: true})
	q2 := &queueCounter{svc: rep2.App.Notify, last: rep2.App.Notify.Queued()}
	var wg sync.WaitGroup
	var total atomic.Int64
	for _, s := range []*notify.Service{svc, rep2.App.Notify, svc, rep2.App.Notify} {
		wg.Add(1)
		go func(s *notify.Service) {
			defer wg.Done()
			n, err := s.Notify(ctx, notify.Notification{Event: notify.SubscriptionPending, Object: "sub-1", URL: "/admin/subscriptions"})
			if err != nil {
				t.Error(err)
			}
			total.Add(int64(n))
		}(s)
	}
	wg.Wait()
	if total.Load() != 1 || q.delta()+q2.delta() != 1 {
		t.Fatalf("cross-replica dedup: %d", total.Load())
	}
}

func TestNotifyUserEvents(t *testing.T) {
	e := setup(t, envOpts{})
	ctx := context.Background()
	svc := e.rep.App.Notify
	svc.AllowAnyEndpoint = true
	fp := newFakePush(t)
	fp.subscribe(t, e.user, "/alice")
	q := &queueCounter{svc: svc, last: svc.Queued()}
	uid := e.userID.String()

	// balance_low: alice starts at $0, below the default $5. A short dedup
	// window shows that the state, not only dedup, stops repeats.
	svc.DedupTTL = 50 * time.Millisecond
	testutil.Must(t, svc.ObserveBalances(ctx))
	if d := q.delta(); d != 1 {
		t.Fatalf("balance_low first drop: %d", d)
	}
	testutil.Eventually(t, 5*time.Second, func() bool { return fp.count("/alice") == 1 }, "delivered")
	testutil.Must(t, svc.ObserveBalances(ctx))
	if d := q.delta(); d != 0 {
		t.Fatalf("still low notified again: %d", d)
	}
	// Even after the dedup window, staying low does not notify.
	time.Sleep(80 * time.Millisecond)
	testutil.Must(t, svc.ObserveBalances(ctx))
	if d := q.delta(); d != 0 {
		t.Fatalf("still low after dedup window: %d", d)
	}
	// Back above the threshold re-arms; the next drop notifies.
	e.admin.must("POST", "/admin/users/"+uid+"/balance", map[string]any{"type": "topup", "amount_micro": 10_000_000, "note": "t"}, 200)
	e.waitOutbox()
	testutil.Must(t, svc.ObserveBalances(ctx))
	if d := q.delta(); d != 0 {
		t.Fatalf("recovery notified: %d", d)
	}
	e.admin.must("POST", "/admin/users/"+uid+"/balance", map[string]any{"type": "adjust", "amount_micro": -8_000_000, "note": "t"}, 200)
	e.waitOutbox()
	time.Sleep(80 * time.Millisecond)
	testutil.Must(t, svc.ObserveBalances(ctx))
	if d := q.delta(); d != 1 {
		t.Fatalf("second drop: %d", d)
	}
	// Turned off: nothing, whatever the balance does.
	e.user.must("PUT", "/notifications/prefs", map[string]any{"items": []any{map[string]any{"event": "balance_low", "enabled": false}}}, 200)
	testutil.Must(t, svc.ObserveBalances(ctx))
	if d := q.delta(); d != 0 {
		t.Fatalf("disabled balance_low: %d", d)
	}
	svc.DedupTTL = 0

	// plan_expiring: once per renewal period.
	_, err := e.rep.App.Pool.Exec(ctx, "UPDATE plan_subscriptions SET expires_at = now() + interval '2 days' WHERE id = $1", e.subID)
	testutil.Must(t, err)
	testutil.Must(t, svc.ObservePlanExpiring(ctx))
	testutil.Must(t, svc.ObservePlanExpiring(ctx))
	if d := q.delta(); d != 1 {
		t.Fatalf("plan_expiring: %d", d)
	}
	// A renewal moves the expiry: a new period, a new notification.
	_, err = e.rep.App.Pool.Exec(ctx, "UPDATE plan_subscriptions SET expires_at = expires_at + interval '1 hour' WHERE id = $1", e.subID)
	testutil.Must(t, err)
	testutil.Must(t, svc.ObservePlanExpiring(ctx))
	testutil.Must(t, svc.ObservePlanExpiring(ctx))
	if d := q.delta(); d != 1 {
		t.Fatalf("plan_expiring next period: %d", d)
	}
	// Further away than 3 days: nothing.
	_, err = e.rep.App.Pool.Exec(ctx, "UPDATE plan_subscriptions SET expires_at = now() + interval '10 days' WHERE id = $1", e.subID)
	testutil.Must(t, err)
	testutil.Must(t, svc.ObservePlanExpiring(ctx))
	if d := q.delta(); d != 0 {
		t.Fatalf("plan far from expiry: %d", d)
	}

	// auto_renew_failed: once per period even though the job retries.
	sub := uuid.MustParse(e.subID)
	svc.AutoRenewFailed(ctx, sub, true)
	svc.AutoRenewFailed(ctx, sub, true)
	if d := q.delta(); d != 1 {
		t.Fatalf("auto_renew_failed: %d", d)
	}

	// key_exhausted: off by default, once per cause when enabled.
	kid := uuid.MustParse(e.keyID)
	svc.KeyExhausted(ctx, kid, "key_quota_exhausted", "", nil)
	if d := q.delta(); d != 0 {
		t.Fatalf("key_exhausted while off: %d", d)
	}
	e.user.must("PUT", "/notifications/prefs", map[string]any{"items": []any{map[string]any{"event": "key_exhausted", "enabled": true}}}, 200)
	rec := time.Now().Add(time.Hour)
	svc.KeyExhausted(ctx, kid, "key_window_exhausted", "5h", &rec)
	svc.KeyExhausted(ctx, kid, "key_window_exhausted", "5h", &rec)
	if d := q.delta(); d != 1 {
		t.Fatalf("key_exhausted: %d", d)
	}
}

// The subscription_pending hook fires on purchase.
func TestNotifyPendingOnPurchase(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 50_000_000, noPlan: true})
	svc := e.rep.App.Notify
	svc.AllowAnyEndpoint = true
	fp := newFakePush(t)
	fp.subscribe(t, e.admin, "/admin")
	plan := e.admin.must("POST", "/admin/plans", map[string]any{"name": "Max", "subscription": "claude",
		"weekly_quota_micro": 1_000_000, "price_micro": 1_000_000, "duration_value": 1, "duration_unit": "month"}, 201)
	e.user.must("POST", "/plans/"+plan["id"].(string)+"/purchase", map[string]any{}, 201)
	testutil.Eventually(t, 5*time.Second, func() bool { return fp.count("/admin") == 1 }, "pending notification delivered")
}
