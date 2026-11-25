package e2e

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/jobs"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/secret"
	"github.com/sketchain/oxalate-bridge/backend/internal/server"
	"github.com/sketchain/oxalate-bridge/backend/internal/settle"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
	"github.com/sketchain/oxalate-bridge/backend/internal/testutil"
)

// newUser creates a user with a topped-up balance directly in the store.
func newUser(t *testing.T, app *server.App, balance int64) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id, _ := uuid.NewV7()
	_, err := app.Store.Q.CreateUser(ctx, sqlc.CreateUserParams{ID: id, Username: "u" + id.String()[:8], PasswordHash: "x",
		Role: "user", MaxConcurrency: 100, SafeguardResetCards: 1})
	testutil.Must(t, err)
	if balance != 0 {
		testutil.Must(t, app.Store.InTx(ctx, func(tx *store.Tx) error {
			_, _, err := tx.AppendLedger(ctx, store.LedgerParams{UserID: id, Type: "admin_topup", Amount: balance,
				IdempotencyKey: "seed:" + id.String(), SyncRedis: true})
			return err
		}))
	}
	return id
}

// syncUser installs the user's committed balance in Redis (as a rebuild does).
func syncUser(t *testing.T, app *server.App, user uuid.UUID) {
	t.Helper()
	row, err := app.Store.Q.GetUserBalance(context.Background(), user)
	testutil.Must(t, err)
	testutil.Must(t, app.St.SyncBase(context.Background(), rstate.UserWallet(user), row.BalanceMicro, row.BalanceVersion))
}

// settleCharge settles one balance-tier request of cost micro on a user.
func settleCharge(t *testing.T, app *server.App, user uuid.UUID, cost int64, held bool) string {
	t.Helper()
	reqID, _ := uuid.NewV7()
	rec := settle.Record{RequestID: reqID, CreatedAt: time.Now().UTC(), UserID: user, KeyID: user, Tier: "standard",
		Endpoint: "/v1/messages", Model: "m", StatusCode: 200, UpstreamCost: cost, Billed: cost, TierMult: 1, ModelMult: 1,
		Wallet: string(rstate.UserWallet(user))}
	raw, _ := json.Marshal(rec)
	id, err := app.St.Settle(context.Background(), rstate.SettleOp{RequestID: reqID.String(), Wallet: rstate.UserWallet(user),
		HasHold: held, Charge: cost, At: time.Now(), Record: raw})
	testutil.Must(t, err)
	return id
}

func TestConcurrentHoldsOverdrawAtMostOnce(t *testing.T) {
	schema := testutil.Schema(t)
	rep := testutil.Start(t, schema, server.Options{DisableJobs: true, DisableConsumer: true})
	app := rep.App
	ctx := context.Background()
	const balance, cost, n = 10_000, 3_000, 50
	user := newUser(t, app, balance)
	// Let the outbox seed the wallet delta (wallet is rebuilt on demand anyway).
	testutil.Eventually(t, 5*time.Second, func() bool {
		var c int
		_ = app.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox").Scan(&c)
		return c == 0
	}, "outbox drained")
	w := rstate.UserWallet(user)
	syncUser(t, app, user)

	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reqID := "req-" + strconv.Itoa(i)
			res, err := app.St.Hold(ctx, w, reqID, cost, time.Minute, time.Now())
			if err != nil {
				t.Error(err)
				return
			}
			if !res.OK {
				return
			}
			mu.Lock()
			admitted++
			mu.Unlock()
			// The real charge equals the estimate: holds never exceed the balance.
			rec := settle.Record{RequestID: uuid.New(), CreatedAt: time.Now().UTC(), UserID: user, KeyID: user, Tier: "standard",
				Endpoint: "/v1/messages", StatusCode: 200, UpstreamCost: cost, Billed: cost, Wallet: string(w), TierMult: 1, ModelMult: 1}
			raw, _ := json.Marshal(rec)
			if _, err := app.St.Settle(ctx, rstate.SettleOp{RequestID: reqID, Wallet: w, HasHold: true, Charge: cost, At: time.Now(), Record: raw}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	avail, err := app.St.Available(ctx, w, time.Now())
	testutil.Must(t, err)
	// 10 000 / 3 000: three full holds, the fourth gets the remaining 1 000.
	if admitted != 4 {
		t.Fatalf("admitted %d", admitted)
	}
	if avail < -cost || avail != balance-int64(admitted)*cost {
		t.Fatalf("available %d after %d charges", avail, admitted)
	}
	// Persist and reconcile.
	for {
		k, err := app.Consumer.ProcessOnce(ctx)
		testutil.Must(t, err)
		if k == 0 {
			break
		}
	}
	bal, _ := app.Store.Q.GetUserBalance(ctx, user)
	if bal.BalanceMicro != avail {
		t.Fatalf("pg balance %d != redis %d", bal.BalanceMicro, avail)
	}
	res, err := jobs.Reconcile(ctx, app.Store, testutil.Logger())
	if err != nil || !res.OK {
		t.Fatalf("reconcile %+v %v", res, err)
	}
}

func TestStreamRedeliveryChargesOnce(t *testing.T) {
	schema := testutil.Schema(t)
	rep := testutil.Start(t, schema, server.Options{DisableJobs: true, DisableConsumer: true})
	app := rep.App
	ctx := context.Background()
	user := newUser(t, app, 100_000)
	w := rstate.UserWallet(user)
	syncUser(t, app, user)
	settleCharge(t, app, user, 700, false)
	settleCharge(t, app, user, 300, false)
	testutil.Must(t, settle.EnsureGroup(ctx, app.St))
	// Read the entries without acking them (a consumer that crashes after commit).
	msgs, err := app.St.R.XRange(ctx, app.St.StreamKey(), "-", "+").Result()
	testutil.Must(t, err)
	if len(msgs) != 2 {
		t.Fatalf("stream entries %d", len(msgs))
	}
	testutil.Must(t, app.Consumer.ProcessMessages(ctx, msgs))
	// Redelivered in a different grouping: one at a time.
	testutil.Must(t, app.Consumer.ProcessMessages(ctx, msgs[:1]))
	testutil.Must(t, app.Consumer.ProcessMessages(ctx, msgs[1:]))
	testutil.Must(t, app.Consumer.ProcessMessages(ctx, msgs))

	var logs, charge int64
	testutil.Must(t, app.Pool.QueryRow(ctx, "SELECT count(*) FROM request_logs").Scan(&logs))
	testutil.Must(t, app.Pool.QueryRow(ctx, "SELECT -sum(amount_micro) FROM ledger_entries WHERE type='api_charge'").Scan(&charge))
	if logs != 2 || charge != 1000 {
		t.Fatalf("logs %d charge %d", logs, charge)
	}
	// The outbox top-up sync may land before or after the consumer's: the
	// wallet converges either way.
	testutil.Eventually(t, 5*time.Second, func() bool {
		var n int
		_ = app.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox").Scan(&n)
		return n == 0
	}, "outbox drained")
	bal, _ := app.Store.Q.GetUserBalance(ctx, user)
	pending, _ := app.St.Pending(ctx, w)
	avail, _ := app.St.Available(ctx, w, time.Now())
	if bal.BalanceMicro != 99_000 || pending != 0 || avail != 99_000 {
		t.Fatalf("balance %d pending %d available %d", bal.BalanceMicro, pending, avail)
	}
}

func TestWalletRebuildAfterRedisLoss(t *testing.T) {
	schema := testutil.Schema(t)
	rep := testutil.Start(t, schema, server.Options{DisableJobs: true, DisableConsumer: true})
	app := rep.App
	ctx := context.Background()
	user := newUser(t, app, 50_000)
	w := rstate.UserWallet(user)
	syncUser(t, app, user)
	drain := func() {
		for {
			k, err := app.Consumer.ProcessOnce(ctx)
			testutil.Must(t, err)
			if k == 0 {
				return
			}
		}
	}
	settleCharge(t, app, user, 4_000, false)
	drain()
	settleCharge(t, app, user, 1_500, false) // still only in the stream

	// Lose the wallet key; the rebuild subtracts what the stream still holds.
	testutil.Must(t, app.St.DropWallet(ctx, w))
	if _, err := app.St.Available(ctx, w, time.Now()); err != rstate.ErrWalletMissing {
		t.Fatalf("wallet not missing: %v", err)
	}
	syncUser(t, app, user)
	avail, err := app.St.Available(ctx, w, time.Now())
	testutil.Must(t, err)
	if avail != 44_500 {
		t.Fatalf("rebuilt %d", avail)
	}
	// A charge settled while the wallet is missing is still accounted for.
	testutil.Must(t, app.St.DropWallet(ctx, w))
	settleCharge(t, app, user, 500, false)
	syncUser(t, app, user)
	if avail, _ = app.St.Available(ctx, w, time.Now()); avail != 44_000 {
		t.Fatalf("rebuilt after missing-wallet charge %d", avail)
	}
	// Persist; a stale (older version) sync arriving late is ignored.
	before, _ := app.Store.Q.GetUserBalance(ctx, user)
	drain()
	testutil.Must(t, app.St.SyncBase(ctx, w, before.BalanceMicro, before.BalanceVersion))
	bal, _ := app.Store.Q.GetUserBalance(ctx, user)
	avail, _ = app.St.Available(ctx, w, time.Now())
	if bal.BalanceMicro != 44_000 || avail != 44_000 {
		t.Fatalf("after persist pg %d redis %d", bal.BalanceMicro, avail)
	}
	// Rebuild racing a commit: the base is read after the commit but the
	// pending decrement has not run yet. The wallet is low until the
	// consumer's own sync lands, then converges.
	settleCharge(t, app, user, 1_000, false)
	testutil.Must(t, app.St.DropWallet(ctx, w))
	msgs, _ := app.St.R.XRange(ctx, app.St.StreamKey(), "-", "+").Result()
	last := msgs[len(msgs)-1:]
	var rec settle.Record
	testutil.Must(t, json.Unmarshal([]byte(last[0].Values["r"].(string)), &rec))
	_, err = app.Pool.Exec(ctx, "UPDATE users SET balance_micro = balance_micro - 1000, balance_version = balance_version + 1 WHERE id = $1", user)
	testutil.Must(t, err)
	syncUser(t, app, user) // reads the "committed" 43 000 while pending is still 1 000
	_, err = app.Pool.Exec(ctx, "UPDATE users SET balance_micro = balance_micro + 1000, balance_version = balance_version - 1 WHERE id = $1", user)
	testutil.Must(t, err)
	drain() // the real commit: same version, pending -1000
	if avail, _ = app.St.Available(ctx, w, time.Now()); avail != 43_000 {
		t.Fatalf("converged %d", avail)
	}
	res, err := jobs.Reconcile(ctx, app.Store, testutil.Logger())
	if err != nil || !res.OK {
		t.Fatalf("reconcile %+v %v", res, err)
	}
}

func TestReconcileDetectsMismatch(t *testing.T) {
	schema := testutil.Schema(t)
	rep := testutil.Start(t, schema, server.Options{DisableJobs: true, DisableConsumer: true})
	app := rep.App
	ctx := context.Background()
	user := newUser(t, app, 10_000)
	res, err := jobs.Reconcile(ctx, app.Store, testutil.Logger())
	if err != nil || !res.OK {
		t.Fatalf("clean reconcile %+v %v", res, err)
	}
	_, err = app.Pool.Exec(ctx, "UPDATE users SET balance_micro = balance_micro + 1 WHERE id = $1", user)
	testutil.Must(t, err)
	res, err = jobs.Reconcile(ctx, app.Store, testutil.Logger())
	if err != nil || res.OK || len(res.BalanceMismatches) != 1 {
		t.Fatalf("mismatch not detected %+v %v", res, err)
	}
}

func TestAuthInvalidationReachesOtherReplica(t *testing.T) {
	schema := testutil.Schema(t)
	a := testutil.Start(t, schema, server.Options{DisableJobs: true, DisableConsumer: true, InstanceID: "a"})
	b := testutil.Start(t, schema, server.Options{DisableJobs: true, DisableConsumer: true, InstanceID: "b"})
	ctx := context.Background()
	user := newUser(t, a.App, 0)
	raw := secret.NewAPIKey()
	id, _ := uuid.NewV7()
	_, err := a.App.Store.Q.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{ID: id, UserID: user, KeyHash: secret.KeyHash(raw),
		KeyEnc: a.App.Box.Seal(raw), KeyPrefix: secret.KeyPrefix(raw), Tiers: []string{"standard"}, Enabled: true, QuotaScope: "all"})
	testutil.Must(t, err)
	// Replica B caches the key (L1 + L2).
	info, err := b.App.Auth.Lookup(ctx, raw)
	testutil.Must(t, err)
	if !info.Usable() {
		t.Fatalf("key not usable: %+v", info)
	}
	// Give B a long L1 so only the invalidation can clear it.
	b.App.Auth.L1TTL = time.Hour
	_, _ = b.App.Auth.Lookup(ctx, raw)
	// Replica A disables the key through the transactional outbox.
	testutil.Must(t, a.App.Store.InTx(ctx, func(tx *store.Tx) error {
		if _, err := tx.Q.UpdateAPIKey(ctx, sqlc.UpdateAPIKeyParams{ID: id, Tiers: []string{"standard"}, Enabled: false, QuotaScope: "all"}); err != nil {
			return err
		}
		return tx.InvalidateHashes(ctx, [][]byte{secret.KeyHash(raw)})
	}))
	testutil.Eventually(t, 5*time.Second, func() bool {
		info, err := b.App.Auth.Lookup(ctx, raw)
		return err == nil && info.Found && !info.KeyEnabled
	}, "replica B still sees the key enabled")
	// The outbox row is gone once delivered.
	var n int
	testutil.Must(t, a.App.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox").Scan(&n))
	if n != 0 {
		t.Fatalf("outbox rows left: %d", n)
	}
	_ = redis.Nil
}
