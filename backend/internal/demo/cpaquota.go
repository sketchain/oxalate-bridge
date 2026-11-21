package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sketchain/oxalate-bridge/backend/internal/cpaquota"
)

// cpaQuota creates a demonstration cpa-quota schema (estimates, k trend,
// reported 5h / 7d windows and requests) so the account detail views have
// data. It does nothing when the schema already exists: real cpa-quota data
// is never touched.
func (s *seeder) cpaQuota(ctx context.Context) error {
	schema := s.app.API.CPAQuota.Schema
	var exists bool
	if err := s.app.Pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", schema).Scan(&exists); err != nil {
		return err
	}
	if exists {
		s.log.Info("cpa-quota schema exists; demo cpa-quota data skipped", "schema", schema)
		return nil
	}
	q := pgx.Identifier{schema}.Sanitize()
	bridge := pgx.Identifier{s.app.Cfg.Schema}.Sanitize()
	conn, err := s.app.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+q+"; SET LOCAL search_path TO "+q+"; "+cpaquota.SchemaSQL); err != nil {
		return fmt.Errorf("create demo cpa-quota schema: %w", err)
	}
	now := time.Now().UTC()
	for i, acc := range s.accounts {
		instance, auth := fmt.Sprintf("demo-%d", i+1), fmt.Sprintf("acct-demo-%d", i+1)
		peak5, peak7 := 55.0+float64(i)*18, 45.0+float64(i)*12
		k := 5.2 + float64(i)*0.6
		detail, _ := json.Marshal(map[string]any{"auth_index": auth, "provider": "claude", "k": k, "demo": true})
		if _, err := tx.Exec(ctx, `INSERT INTO `+q+`.estimates (instance, auth_index, provider, subscription, tier, state, segment_window, basis,
			k, k_source, totals_from, safety_factor, used_5h, reset_5h, used_7d, reset_7d, five_hour_total_usd, weekly_total_usd,
			remaining_five_hour_usd, remaining_week_usd, clean_blocks, total_blocks, segments, excluded_segments, ratio, observed_at,
			estimated_at, detail)
			VALUES ($1, $2, 'claude', 'claude', 'max5x', 'estimated', '5h', 'raw', $3, 'fit', 'tier', 0.9, $4, $5, $6, $7, 10, 100,
			$8, $9, 12, 14, 240, 6, 1.02, $10, $10, $11)`,
			instance, auth, k, peak5/2, now.Add(3*time.Hour), peak7/2, now.Add(4*24*time.Hour), 9*(1-peak5/200), 90*(1-peak7/200), now, detail); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO `+q+`.k_trend_daily (instance, auth_index, day, gain_5h, gain_7d, k, segments, excluded_segments, cost_usd, gain)
			SELECT $1, $2, d::date, 40 + random() * 20, 6 + random() * 3, $3 + (random() - 0.5) * 0.8, 8 + (random() * 6)::int, (random() * 2)::int,
			       5 + random() * 8, 1 + random()
			FROM generate_series(current_date - 29, current_date, interval '1 day') d`, instance, auth, k); err != nil {
			return err
		}
		// Sawtooth windows: usage climbs within each window and drops at its reset.
		for _, w := range []struct {
			seconds, step, span int
			peak                float64
		}{{18000, 600, 7 * 86400, peak5}, {604800, 3600, 30 * 86400, peak7}} {
			anchor := now.Unix() - int64(i*4000) - int64(w.seconds/3)
			if _, err := tx.Exec(ctx, `WITH pts AS (
					SELECT (extract(epoch FROM t))::bigint AS s
					FROM generate_series(now() - make_interval(secs => $6::float8), now(), make_interval(secs => $5::float8)) t
				), r AS (
					INSERT INTO `+q+`.quota_reports (instance, auth_index, provider, at_ns)
					SELECT $1::text, $2::text, 'claude', s * 1000000000 FROM pts RETURNING id, at_ns
				), p AS (
					SELECT id, at_ns, mod(at_ns / 1000000000 - $4::bigint + $3::bigint * 1000, $3::bigint) AS phase FROM r
				)
				INSERT INTO `+q+`.quota_report_windows (report_id, instance, auth_index, provider, limit_key, seconds, at_ns, used_percent, reset_ns)
				SELECT id, $1::text, $2::text, 'claude', 'unified', $3::bigint, at_ns,
				       least(100, (phase::float8 / $3::bigint) * $7::float8 + random() * 2),
				       (at_ns / 1000000000 + $3::bigint - phase) * 1000000000
				FROM p`, instance, auth, int64(w.seconds), anchor, w.step, w.span, w.peak); err != nil {
				return err
			}
		}
		// cpa-quota's view of the requests the bridge sent (a few percent apart).
		if _, err := tx.Exec(ctx, `INSERT INTO `+q+`.requests (instance, auth_index, provider, request_id, requested_ns, completed_ns, model,
				input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, accounting, cost_usd, priced, main_window, status_code)
			SELECT $1, $2, 'claude', request_id::text, (extract(epoch FROM created_at) * 1e9)::bigint,
			       (extract(epoch FROM created_at) * 1e9)::bigint + latency_ms::bigint * 1000000, model, input_tokens, cache_read_tokens,
			       cache_write_tokens, output_tokens, 'complete', upstream_cost_micro / 1e6 * (0.97 + random() * 0.08), true, true, status_code
			FROM `+bridge+`.request_logs WHERE account_id = $3`, instance, auth, acc); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
