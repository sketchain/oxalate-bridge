// Package testutil starts bridge replicas against the PostgreSQL and Redis
// named by BRIDGE_TEST_DATABASE_URL and BRIDGE_TEST_REDIS_URL. Each test gets
// its own schema (and therefore Redis key prefix); tests skip when the
// variables are unset.
package testutil

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/sketchain/oxalate-bridge/backend/internal/config"
	"github.com/sketchain/oxalate-bridge/backend/internal/server"
)

// SampleTable is cpa-quota's test ratio table.
const SampleTable = `{"success":true,"data":{
	"model_ratio":{"claude-opus-4-8":2.5,"Claude-Sonnet-4.6":1.5,"gpt-5.6-sol":2,"vendor/bare-model":1},
	"completion_ratio":{"claude-opus-4-8":5,"Claude-Sonnet-4.6":5,"gpt-5.6-sol":8},
	"cache_ratio":{"claude-opus-4-8":0.1,"gpt-5.6-sol":0.1},
	"create_cache_ratio":{"claude-opus-4-8":1.25}}}`

// Env returns the test DSNs or skips the test.
func Env(t testing.TB) (string, string) {
	t.Helper()
	dsn, rurl := os.Getenv("BRIDGE_TEST_DATABASE_URL"), os.Getenv("BRIDGE_TEST_REDIS_URL")
	if dsn == "" || rurl == "" {
		t.Skip("BRIDGE_TEST_DATABASE_URL / BRIDGE_TEST_REDIS_URL not set")
	}
	return dsn, rurl
}

// Config builds a config for a fresh schema.
func Config(t testing.TB, schema string) config.Config {
	dsn, rurl := Env(t)
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cfg, err := config.FromLookup(func(k string) (string, bool) {
		switch k {
		case "BRIDGE_DATABASE_URL":
			return dsn, true
		case "BRIDGE_REDIS_URL":
			return rurl, true
		case "BRIDGE_SCHEMA":
			return schema, true
		case "BRIDGE_SECRET_KEY":
			return key, true
		case "BRIDGE_COOKIE_SECURE":
			return "false", true
		case "BRIDGE_TRUSTED_PROXIES":
			return "127.0.0.1/32", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Logger returns a quiet logger (set BRIDGE_TEST_LOG=1 for output).
func Logger() *slog.Logger {
	if os.Getenv("BRIDGE_TEST_LOG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Schema returns a unique schema name and registers its cleanup.
func Schema(t testing.TB) string {
	t.Helper()
	schema := "t_" + strings.ReplaceAll(uuid.NewString()[:12], "-", "")
	dsn, rurl := Env(t)
	t.Cleanup(func() {
		ctx := context.Background()
		if conn, err := pgx.Connect(ctx, dsn); err == nil {
			_, _ = conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
			_ = conn.Close(ctx)
		}
		if opts, err := redis.ParseURL(rurl); err == nil {
			r := redis.NewClient(opts)
			iter := r.Scan(ctx, 0, "ob:"+schema+":*", 1000).Iterator()
			for iter.Next(ctx) {
				r.Del(ctx, iter.Val())
			}
			_ = r.Close()
		}
	})
	return schema
}

// Replica is a started App with an HTTP server.
type Replica struct {
	App    *server.App
	Server *httptest.Server
}

// URL is the replica's base URL.
func (r *Replica) URL() string { return r.Server.URL }

// Start starts a replica on schema. The price table is SampleTable.
func Start(t testing.TB, schema string, opts server.Options) *Replica {
	t.Helper()
	cfg := Config(t, schema)
	cfg.BootstrapAdminUsername, cfg.BootstrapAdminPassword = "admin", "admin-password"
	opts.SkipPriceFetch = true
	ctx := context.Background()
	app, err := server.New(ctx, cfg, Logger(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Prices.SetBody([]byte(SampleTable), "database", "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	app.Start(ctx)
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(func() {
		srv.Close()
		app.Close()
	})
	return &Replica{App: app, Server: srv}
}

// Eventually polls cond until it holds or the timeout passes.
func Eventually(t testing.TB, timeout time.Duration, cond func() bool, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met: "+format, args...)
}

// Must fails the test on error.
func Must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Sprintf is fmt.Sprintf (keeps imports tidy in callers).
func Sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
