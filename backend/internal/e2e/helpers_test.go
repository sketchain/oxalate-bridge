package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sketchain/oxalate-bridge/backend/internal/fakecpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/server"
	"github.com/sketchain/oxalate-bridge/backend/internal/testutil"
)

// client is a panel API client with a cookie jar.
type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, c.base+"/api/v1"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "test")
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: decode %s: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode
}

func (c *client) must(method, path string, body any, want int) map[string]any {
	c.t.Helper()
	var out map[string]any
	var raw json.RawMessage
	if got := c.do(method, path, body, &raw); got != want {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, got, want, raw)
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func (c *client) login(username, password string) {
	c.t.Helper()
	c.must("POST", "/auth/login", map[string]any{"username": username, "password": password}, 200)
}

// env is a running replica with two fake CPA accounts and a user.
type env struct {
	t      *testing.T
	rep    *testutil.Replica
	admin  *client
	user   *client
	userID uuid.UUID
	cpaA   *fakecpa.CPA
	cpaB   *fakecpa.CPA
	accA   string
	accB   string
	planID string
	subID  string
	key    string
	keyID  string
	schema string
}

type envOpts struct {
	balanceMicro int64
	planQuota    int64
	allowed      []string
	tiers        []string
	noPlan       bool
	secondAcct   bool
}

func setup(t *testing.T, o envOpts) *env {
	t.Helper()
	schema := testutil.Schema(t)
	rep := testutil.Start(t, schema, server.Options{DisableJobs: true, DisableConsumer: true})
	e := &env{t: t, rep: rep, schema: schema}
	e.cpaA = fakecpa.New("a")
	t.Cleanup(e.cpaA.Close)
	e.admin = newClient(t, rep.URL())
	e.admin.login("admin", "admin-password")
	acc := e.admin.must("POST", "/admin/accounts", map[string]any{"name": "A", "base_url": e.cpaA.URL(),
		"cpa_key": e.cpaA.Key, "quota_token": e.cpaA.QuotaToken}, 201)
	e.accA = acc["id"].(string)
	if o.secondAcct {
		e.cpaB = fakecpa.New("b")
		t.Cleanup(e.cpaB.Close)
		acc := e.admin.must("POST", "/admin/accounts", map[string]any{"name": "B", "base_url": e.cpaB.URL(),
			"cpa_key": e.cpaB.Key, "quota_token": e.cpaB.QuotaToken}, 201)
		e.accB = acc["id"].(string)
	}
	u := e.admin.must("POST", "/admin/users", map[string]any{"username": "alice", "password": "alice-password"}, 201)
	e.userID = uuid.MustParse(u["id"].(string))
	if o.balanceMicro != 0 {
		e.admin.must("POST", "/admin/users/"+u["id"].(string)+"/balance",
			map[string]any{"type": "topup", "amount_micro": o.balanceMicro, "note": "test"}, 200)
	}
	keyBody := map[string]any{}
	if !o.noPlan {
		quota := o.planQuota
		if quota == 0 {
			quota = 10_000_000
		}
		plan := e.admin.must("POST", "/admin/plans", map[string]any{"name": "Pro", "subscription": "claude",
			"weekly_quota_micro": quota, "price_micro": 0, "duration_value": 1, "duration_unit": "month",
			"allowed_models": o.allowed}, 201)
		e.planID = plan["id"].(string)
		grant := e.admin.must("POST", "/admin/subscriptions", map[string]any{"user_id": u["id"], "plan_id": e.planID,
			"account_id": e.accA}, 201)
		e.subID = grant["subscription"].(map[string]any)["id"].(string)
		keyBody["subscription_id"] = e.subID
	}
	if o.tiers != nil {
		keyBody["tiers"] = o.tiers
	}
	e.user = newClient(t, rep.URL())
	e.user.login("alice", "alice-password")
	key := e.user.must("POST", "/keys", keyBody, 201)
	e.key, e.keyID = key["key"].(string), key["id"].(string)
	// The outbox and catalog run in the background; wait for both.
	e.waitOutbox()
	ctx := context.Background()
	testutil.Must(t, rep.App.Cat.ReloadDB(ctx))
	testutil.Must(t, rep.App.Cat.ReloadRedis(ctx))
	return e
}

func (e *env) waitOutbox() {
	e.t.Helper()
	testutil.Eventually(e.t, 5*time.Second, func() bool {
		var n int
		_ = e.rep.App.Pool.QueryRow(context.Background(), "SELECT count(*) FROM outbox").Scan(&n)
		return n == 0
	}, "outbox drained")
}

// relay sends a /v1 request with the user's key.
func (e *env) relay(path string, body []byte, headers map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.rep.URL()+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.key)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

// persist drains the settlement stream into PostgreSQL.
func (e *env) persist() {
	e.t.Helper()
	for {
		n, err := e.rep.App.Consumer.ProcessOnce(context.Background())
		testutil.Must(e.t, err)
		if n == 0 {
			return
		}
	}
}

func (e *env) scalar(query string, args ...any) int64 {
	e.t.Helper()
	var v int64
	testutil.Must(e.t, e.rep.App.Pool.QueryRow(context.Background(), query, args...).Scan(&v))
	return v
}
