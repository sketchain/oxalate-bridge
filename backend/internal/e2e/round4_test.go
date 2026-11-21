package e2e

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

// bindParts is the part of a bind response a dry run must reproduce.
func bindParts(t *testing.T, raw string) string {
	t.Helper()
	out, _ := json.Marshal(map[string]any{"capacity": gjson.Get(raw, "capacity").Value(), "warnings": gjson.Get(raw, "warnings").Value(),
		"warning": gjson.Get(raw, "warning").Value()})
	return string(out)
}

func (c *client) raw(method, path string, body any, want int) string {
	c.t.Helper()
	var raw json.RawMessage
	if got := c.do(method, path, body, &raw); got != want {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, got, want, raw)
	}
	return string(raw)
}

// Assign, rebind and grant take dry_run: it validates exactly like the real
// call, changes nothing, and returns the same capacity and warnings.
func TestBindDryRun(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 5_000_000, planQuota: 60_000_000, secondAcct: true})
	big := e.admin.must("POST", "/admin/plans", map[string]any{"name": "Big", "subscription": "claude",
		"weekly_quota_micro": 50_000_000, "price_micro": 0, "duration_value": 1, "duration_unit": "week"}, 201)
	bigID := big["id"].(string)
	bought := e.user.must("POST", "/plans/"+bigID+"/purchase", nil, 201)
	pending := bought["id"].(string)
	countAudit := func() int64 { return e.scalar("SELECT count(*) FROM " + e.schema + ".audit_logs") }
	countSubs := func() int64 { return e.scalar("SELECT count(*) FROM " + e.schema + ".plan_subscriptions") }
	audit0, subs0 := countAudit(), countSubs()

	// Assign to A (already 60 sold of a 100 week total): overcommit.
	dry := e.admin.raw("POST", "/admin/subscriptions/"+pending+"/assign?dry_run=true", map[string]any{"account_id": e.accA}, 200)
	if !gjson.Get(dry, "dry_run").Bool() || gjson.Get(dry, "subscription").Type != gjson.Null {
		t.Fatalf("dry run %s", dry)
	}
	if gjson.Get(dry, "capacity.sold_weekly_quota_micro").Int() != 110_000_000 || gjson.Get(dry, "capacity.subscription_quota_micro").Int() != 50_000_000 ||
		gjson.Get(dry, "capacity.week_total_micro").Int() != 100_000_000 || gjson.Get(dry, "capacity.account_id").String() != e.accA {
		t.Fatalf("dry run capacity %s", dry)
	}
	if gjson.Get(dry, `warnings.#(code=="overcommit")`).Type == gjson.Null {
		t.Fatalf("no overcommit warning %s", dry)
	}
	if status := gjson.Get(rawGet(t, e.admin, "/admin/subscriptions?status=pending_assignment"), "items.#(id==\""+pending+"\").status").String(); status != "pending_assignment" {
		t.Fatalf("dry run changed the subscription: %q", status)
	}
	if countAudit() != audit0 || countSubs() != subs0 {
		t.Fatal("dry run wrote something")
	}
	done := e.admin.raw("POST", "/admin/subscriptions/"+pending+"/assign", map[string]any{"account_id": e.accA}, 200)
	if gjson.Get(done, "dry_run").Bool() || gjson.Get(done, "subscription.status").String() != "active" {
		t.Fatalf("assign %s", done)
	}
	if bindParts(t, dry) != bindParts(t, done) {
		t.Fatalf("dry run and real assign differ:\n%s\n%s", bindParts(t, dry), bindParts(t, done))
	}
	// A dry run validates like the real call: the subscription is no longer pending.
	e.admin.must("POST", "/admin/subscriptions/"+pending+"/assign?dry_run=true", map[string]any{"account_id": e.accA}, 409)

	// Rebind it to B: 50 of 100, no overcommit.
	dry = e.admin.raw("POST", "/admin/subscriptions/"+pending+"/rebind?dry_run=true", map[string]any{"account_id": e.accB}, 200)
	if gjson.Get(dry, "capacity.sold_weekly_quota_micro").Int() != 50_000_000 || gjson.Get(dry, `warnings.#(code=="overcommit")`).Type != gjson.Null {
		t.Fatalf("rebind dry run %s", dry)
	}
	if acc := gjson.Get(rawGet(t, e.admin, "/admin/subscriptions?status=active"), "items.#(id==\""+pending+"\").account_id").String(); acc != e.accA {
		t.Fatalf("rebind dry run moved the subscription to %s", acc)
	}
	done = e.admin.raw("POST", "/admin/subscriptions/"+pending+"/rebind", map[string]any{"account_id": e.accB}, 200)
	if bindParts(t, dry) != bindParts(t, done) || gjson.Get(done, "subscription.account_id").String() != e.accB {
		t.Fatalf("rebind dry run / real differ:\n%s\n%s", dry, done)
	}

	// Grant on B: dry run 200 without creating anything, done 201 with the same warnings.
	body := map[string]any{"user_id": e.userID, "plan_id": bigID, "account_id": e.accB, "charge": true}
	balance0 := gjson.Get(rawGet(t, e.user, "/balance"), "balance_micro").Int()
	subs1 := countSubs()
	dry = e.admin.raw("POST", "/admin/subscriptions?dry_run=true", body, 200)
	if gjson.Get(dry, "capacity.sold_weekly_quota_micro").Int() != 100_000_000 || gjson.Get(dry, "subscription").Type != gjson.Null {
		t.Fatalf("grant dry run %s", dry)
	}
	if countSubs() != subs1 || gjson.Get(rawGet(t, e.user, "/balance"), "balance_micro").Int() != balance0 {
		t.Fatal("grant dry run wrote something")
	}
	done = e.admin.raw("POST", "/admin/subscriptions", body, 201)
	if bindParts(t, dry) != bindParts(t, done) || countSubs() != subs1+1 {
		t.Fatalf("grant dry run / real differ:\n%s\n%s", dry, done)
	}
	// Validation errors are the same too.
	e.admin.must("POST", "/admin/subscriptions?dry_run=true", map[string]any{"user_id": e.userID, "plan_id": bigID,
		"account_id": "00000000-0000-7000-8000-000000000000"}, 404)
}

// Oversold accounts (H_A < 0) are counted apart and never offset the other
// accounts' headroom; the account list carries the same summary as the
// overview.
func TestOversoldSummary(t *testing.T) {
	e := setup(t, envOpts{balanceMicro: 1_000_000, planQuota: 200_000_000, secondAcct: true})
	list := rawGet(t, e.admin, "/admin/accounts")
	a := gjson.Get(list, `items.#(id=="`+e.accA+`").runtime`)
	b := gjson.Get(list, `items.#(id=="`+e.accB+`").runtime`)
	if a.Get("headroom_micro").Int() >= 0 || !a.Get("oversold").Bool() || b.Get("headroom_micro").Int() <= 0 || b.Get("oversold").Bool() {
		t.Fatalf("headroom A %s B %s", a.Raw, b.Raw)
	}
	sum := gjson.Get(list, "summary")
	if sum.Get("headroom_micro").Int() != b.Get("headroom_micro").Int() || sum.Get("oversold_count").Int() != 1 ||
		sum.Get("oversold_micro").Int() != -a.Get("headroom_micro").Int() {
		t.Fatalf("summary %s", sum.Raw)
	}
	ov := gjson.Get(rawGet(t, e.admin, "/admin/overview"), "accounts")
	if ov.Raw != sum.Raw {
		t.Fatalf("overview accounts %s != list summary %s", ov.Raw, sum.Raw)
	}
	if a.Get("display_status").String() == "" || a.Get("status_reason_code").Type == gjson.Null {
		t.Fatalf("display fields %s", a.Raw)
	}
}
