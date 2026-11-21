package e2e

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/tidwall/gjson"

	"github.com/sketchain/oxalate-bridge/backend/internal/demo"
	"github.com/sketchain/oxalate-bridge/backend/internal/fakecpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/jobs"
	"github.com/sketchain/oxalate-bridge/backend/internal/server"
	"github.com/sketchain/oxalate-bridge/backend/internal/testutil"
)

// bridge seed-demo against four drifting fake CPAs: users, accounts, plans,
// one pending subscription, live and historical requests that reconcile, and
// a demo cpa-quota schema. A second run does nothing.
func TestSeedDemo(t *testing.T) {
	schema := testutil.Schema(t)
	rep := testutil.Start(t, schema, server.Options{DisableJobs: true})
	ctx := context.Background()
	cq := schema + "_cq"
	rep.App.API.CPAQuota.Schema = cq
	t.Cleanup(func() {
		_, _ = rep.App.Pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{cq}.Sanitize()+" CASCADE")
	})
	var urls []string
	for i := 1; i <= 4; i++ {
		c := fakecpa.NewUnstarted(fmt.Sprintf("demo-%d", i))
		c.Drift = true
		c.Server = httptest.NewServer(c)
		t.Cleanup(c.Close)
		urls = append(urls, c.URL())
	}
	opts := demo.Options{URLs: urls, AdminUsername: "admin", AdminPassword: "admin-password"}
	testutil.Must(t, demo.Seed(ctx, rep.App, testutil.Logger(), opts))

	admin := newClient(t, rep.URL())
	admin.login("admin", "admin-password")
	if n := gjson.Get(rawGet(t, admin, "/admin/accounts"), "items.#").Int(); n != 4 {
		t.Fatalf("accounts %d", n)
	}
	ov := rawGet(t, admin, "/admin/overview")
	if gjson.Get(ov, "pending_assignment").Int() != 1 || !gjson.Get(ov, "last_reconcile.ok").Bool() || gjson.Get(ov, "today.requests").Int() < 21 {
		t.Fatalf("overview %s", ov)
	}
	if k := gjson.Get(rawGet(t, admin, "/admin/accounts"), "items.0.k"); k.Type == gjson.Null {
		t.Fatal("demo cpa-quota estimates missing")
	}
	alice := newClient(t, rep.URL())
	alice.login("alice", "demo-pass-123")
	if n := gjson.Get(rawGet(t, alice, "/keys"), "items.#").Int(); n != 2 {
		t.Fatalf("alice keys %d", n)
	}
	if n := gjson.Get(rawGet(t, alice, "/usage/logs?limit=200"), "items.#").Int(); n < 50 {
		t.Fatalf("alice history %d", n)
	}
	res, err := jobs.Reconcile(ctx, rep.App.Store, testutil.Logger())
	testutil.Must(t, err)
	if !res.OK {
		t.Fatalf("reconcile after seeding %+v", res)
	}
	if err := demo.Seed(ctx, rep.App, testutil.Logger(), opts); !errors.Is(err, demo.ErrAlreadySeeded) {
		t.Fatalf("second run: %v", err)
	}
}
