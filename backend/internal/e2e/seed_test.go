package e2e

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"testing"
	"time"

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
		c.ApplyDemo(fakecpa.DemoProfileFor(i), time.Now().UTC())
		c.Drift = true
		c.Server = httptest.NewServer(c)
		t.Cleanup(c.Close)
		urls = append(urls, c.URL())
	}
	opts := demo.Options{URLs: urls, AdminUsername: "admin", AdminPassword: "admin-password"}
	testutil.Must(t, demo.Seed(ctx, rep.App, testutil.Logger(), opts))

	admin := newClient(t, rep.URL())
	admin.login("admin", "admin-password")
	// Notification preferences are stored with their defaults (11 admin
	// events + 4 per user for alice, bob and carol).
	var prefs, thr int
	testutil.Must(t, rep.App.Pool.QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE threshold_micro = 20000000) FROM notification_prefs").Scan(&prefs, &thr))
	if prefs != 11+3*4 || thr != 1 {
		t.Fatalf("notification prefs %d (threshold rows %d)", prefs, thr)
	}
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
	// The demo estimates and the runtime come from the same fake profile:
	// tier, totals, safety factor, used percentages and resets agree.
	for i, acc := range gjson.Get(rawGet(t, admin, "/admin/accounts"), "items").Array() {
		prof := fakecpa.DemoProfileFor(i + 1)
		est := rawGet(t, admin, "/admin/accounts/"+acc.Get("id").String()+"/estimate")
		rt := acc.Get("runtime")
		if acc.Get("tier").String() != prof.Tier || gjson.Get(est, "tier").String() != prof.Tier {
			t.Fatalf("account %d tier %s / estimate %s, profile %s", i+1, acc.Get("tier"), gjson.Get(est, "tier"), prof.Tier)
		}
		pairs := [][2]float64{
			{gjson.Get(est, "five_hour_total_usd").Float(), rt.Get("five_hour.total_usd").Float()},
			{gjson.Get(est, "weekly_total_usd").Float(), rt.Get("week.total_usd").Float()},
			{gjson.Get(est, "five_hour_total_usd").Float(), prof.FiveTotal},
			{gjson.Get(est, "weekly_total_usd").Float(), prof.WeekTotal},
			{gjson.Get(est, "safety_factor").Float(), rt.Get("safety_factor").Float()},
			{gjson.Get(est, "used_5h").Float(), rt.Get("five_hour.used_percent").Float()},
			{gjson.Get(est, "used_7d").Float(), rt.Get("week.used_percent").Float()},
			{gjson.Get(est, "remaining_week_usd").Float(), rt.Get("week.remaining_usd").Float()},
			{gjson.Get(est, "k").Float(), acc.Get("k").Float()},
			{gjson.Get(est, "k").Float(), prof.K()},
		}
		// k = week total ÷ 5h total, a plausible 5–10 (not a per-percent figure).
		if k := acc.Get("k").Float(); k < 5 || k > 10 || math.Abs(k-rt.Get("week.total_usd").Float()/rt.Get("five_hour.total_usd").Float()) > 1e-6 {
			t.Fatalf("account %d k %v", i+1, k)
		}
		for j, p := range pairs {
			if math.Abs(p[0]-p[1]) > 1e-6 {
				t.Fatalf("account %d pair %d: estimate %v runtime/profile %v\nestimate %s\nruntime %s", i+1, j, p[0], p[1], est, rt.Raw)
			}
		}
		if !gjson.Get(est, "reset_7d").Time().Equal(rt.Get("week.reset_at").Time()) {
			t.Fatalf("account %d week reset %s vs %s", i+1, gjson.Get(est, "reset_7d"), rt.Get("week.reset_at"))
		}
	}
	alice := newClient(t, rep.URL())
	alice.login("alice", "demo-pass-123")
	if n := gjson.Get(rawGet(t, alice, "/keys"), "items.#").Int(); n != 2 {
		t.Fatalf("alice keys %d", n)
	}
	if n := gjson.Get(rawGet(t, alice, "/usage/logs?limit=200"), "items.#").Int(); n < 50 {
		t.Fatalf("alice history %d", n)
	}
	// A key with limits whose meter includes the history, a redeem batch, an announcement.
	keys := gjson.Get(rawGet(t, alice, "/keys"), "items").Array()
	limited := 0
	for _, k := range keys {
		if k.Get("limit_1d_micro").Exists() && k.Get("limit_1d_micro").Type != gjson.Null {
			limited++
			lim := gjson.Parse(rawGet(t, alice, "/keys/"+k.Get("id").String()+"/limits"))
			if lim.Get("used_all_micro").Int() <= 0 || lim.Get("windows.2.used_micro").Int() <= 0 {
				t.Fatalf("limited key meter %s", lim.Raw)
			}
		}
	}
	if limited != 2 {
		t.Fatalf("limited keys %d", limited)
	}
	if b := gjson.Get(rawGet(t, admin, "/admin/redeem-batches"), "items.0"); b.Get("count").Int() != 20 || b.Get("used").Int() != 1 || b.Get("voided").Int() != 1 {
		t.Fatalf("redeem batch %s", b.Raw)
	}
	if n := gjson.Get(rawGet(t, alice, "/announcements"), "items.#").Int(); n != 1 {
		t.Fatalf("announcements %d", n)
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
