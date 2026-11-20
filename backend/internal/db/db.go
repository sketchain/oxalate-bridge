// Package db opens the PostgreSQL pool, runs the embedded goose migrations
// and maintains the monthly request_logs partitions.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open connects a pool whose connections use schema as the search path, so
// the unqualified sqlc queries address the bridge schema.
func Open(ctx context.Context, url, schema string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	if cfg.MaxConns < 10 {
		cfg.MaxConns = 20
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate creates the schema if needed and applies the embedded migrations
// under an advisory lock so concurrently starting replicas serialize.
func Migrate(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtext('oxalate-bridge:migrate'))"); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(hashtext('oxalate-bridge:migrate'))") //nolint:errcheck
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	return runGoose(ctx, db, schema)
}

func runGoose(ctx context.Context, db *sql.DB, schema string) error {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	store, err := database.NewStore(goose.DialectPostgres, schema+".goose_db_version")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider("", db, sub, goose.WithStore(store), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}

// EnsurePartitions creates request_logs partitions for the month of now and
// the following months ahead.
func EnsurePartitions(ctx context.Context, pool *pgxpool.Pool, now time.Time, ahead int) error {
	start := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= ahead; i++ {
		from := start.AddDate(0, i, 0)
		to := from.AddDate(0, 1, 0)
		name := fmt.Sprintf("request_logs_%04d%02d", from.Year(), from.Month())
		stmt := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s PARTITION OF request_logs FOR VALUES FROM ('%s') TO ('%s')",
			pgx.Identifier{name}.Sanitize(), from.Format(time.RFC3339), to.Format(time.RFC3339))
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("create partition %s: %w", name, err)
		}
	}
	return nil
}
