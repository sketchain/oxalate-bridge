package cpaquota_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sketchain/oxalate-bridge/backend/internal/cpaquota"
)

// upstreamSchemaEnv names a schema migrated by the pinned cpa-quota release
// (UPSTREAM). The Check workflow creates it; without it only the reference
// copy is checked.
const upstreamSchemaEnv = "BRIDGE_TEST_CPA_QUOTA_SCHEMA"

// TestUpstreamSchemaDrift keeps schema.sql, the bridge's queries and the
// real plugin in step: every query in queries/cpaquota.sql must prepare
// against the reference schema, and, when an upstream-migrated schema is
// given, against it too, with identical columns and indexes.
func TestUpstreamSchemaDrift(t *testing.T) {
	dsn := os.Getenv("BRIDGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BRIDGE_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	var b [6]byte
	_, _ = rand.Read(b[:])
	ref := "cpaq_drift_" + hex.EncodeToString(b[:])
	q := pgx.Identifier{ref}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), "DROP SCHEMA "+q+" CASCADE") })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+q+"; "+cpaquota.SchemaSQL); err != nil {
		t.Fatalf("apply schema.sql: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	queries := loadQueries(t)
	prepareAll(ctx, t, conn, ref, queries)

	upstream := os.Getenv(upstreamSchemaEnv)
	if upstream == "" {
		t.Logf("%s not set; skipped the comparison with the pinned cpa-quota release", upstreamSchemaEnv)
		return
	}
	prepareAll(ctx, t, conn, upstream, queries)

	for _, c := range []struct{ name, sql string }{
		{"columns", `SELECT table_name || '.' || column_name || ' ' || data_type || ' null=' || is_nullable ||
			' generated=' || is_generated || ' ' || COALESCE(generation_expression, '') ||
			' default=' || COALESCE(regexp_replace(column_default, '^nextval\(.*\)$', 'nextval'), '')
			FROM information_schema.columns WHERE table_schema = $1`},
		{"indexes", `SELECT indexname || ' ' || replace(indexdef, quote_ident(schemaname) || '.', '')
			FROM pg_indexes WHERE schemaname = $1`},
		{"meta", `SELECT 'schema_version=' || value FROM meta WHERE key = 'schema_version'`},
	} {
		want := describe(ctx, t, conn, upstream, c.sql)
		got := describe(ctx, t, conn, ref, c.sql)
		if missing, extra := diff(want, got); len(missing)+len(extra) > 0 {
			t.Errorf("schema.sql %s drifted from cpa-quota %s:\n  only upstream:\n    %s\n  only schema.sql:\n    %s",
				c.name, upstreamRef(t), strings.Join(missing, "\n    "), strings.Join(extra, "\n    "))
		}
	}
}

var queryName = regexp.MustCompile(`(?m)^-- name: (\S+)`)

// loadQueries splits queries/cpaquota.sql into its named statements.
func loadQueries(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("queries/cpaquota.sql")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	locs := queryName.FindAllStringSubmatchIndex(src, -1)
	if len(locs) == 0 {
		t.Fatal("no queries found")
	}
	out := map[string]string{}
	for i, l := range locs {
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := src[l[1]:end]
		if j := strings.IndexByte(body, '\n'); j >= 0 {
			body = body[j+1:] // drop the rest of the name line (:one, :many)
		}
		out[src[l[2]:l[3]]] = sqlcArgs.ReplaceAllString(strings.TrimSpace(body), "$$1")
	}
	return out
}

// sqlcArgs matches sqlc.arg/narg macros; the queries here use positional
// parameters, so any macro is only a placeholder for preparation.
var sqlcArgs = regexp.MustCompile(`sqlc\.n?arg\([^)]*\)`)

// prepareAll prepares every query with the schema as search path, which
// resolves every table and column without running anything.
func prepareAll(ctx context.Context, t *testing.T, conn *pgx.Conn, schema string, queries map[string]string) {
	t.Helper()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(queries))
	for n := range queries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, err := tx.Conn().Prepare(ctx, "", queries[n]); err != nil {
			t.Errorf("query %s does not prepare against schema %s: %v", n, schema, err)
		}
	}
}

func describe(ctx context.Context, t *testing.T, conn *pgx.Conn, schema, query string) []string {
	t.Helper()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	var args []any
	if strings.Contains(query, "$1") {
		args = append(args, schema)
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		t.Fatalf("describe %s: %v", schema, err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("describe %s: %v", schema, err)
	}
	sort.Strings(out)
	return out
}

func diff(want, got []string) (missing, extra []string) {
	seen := map[string]int{}
	for _, s := range got {
		seen[s]++
	}
	for _, s := range want {
		if seen[s] > 0 {
			seen[s]--
			continue
		}
		missing = append(missing, s)
	}
	for _, s := range got {
		if seen[s] > 0 {
			seen[s]--
			extra = append(extra, s)
		}
	}
	return missing, extra
}

func upstreamRef(t *testing.T) string {
	raw, err := os.ReadFile("UPSTREAM")
	if err != nil {
		return "(UPSTREAM unreadable)"
	}
	return fmt.Sprintf("%.7s", strings.TrimSpace(string(raw)))
}
