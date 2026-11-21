-- Read-only queries against the cpa-quota schema. They are unqualified and
-- run inside a read-only transaction whose search_path is
-- BRIDGE_CPA_QUOTA_SCHEMA (see cpaquota.Reader).

-- name: GetEstimate :one
SELECT * FROM estimates WHERE instance = $1 AND auth_index = $2;

-- name: ListKTrend :many
SELECT day, gain_5h, gain_7d, k, segments, excluded_segments, cost_usd, gain, updated_at
FROM k_trend_daily
WHERE instance = $1 AND auth_index = $2 AND day >= sqlc.arg('since')::date
ORDER BY day;

-- name: CountWindowPoints :one
SELECT count(*)::bigint FROM quota_report_windows
WHERE instance = $1 AND auth_index = $2 AND limit_key = 'unified' AND seconds = sqlc.arg('seconds')::bigint
  AND at_ns >= sqlc.arg('from_ns')::bigint AND at_ns < sqlc.arg('to_ns')::bigint;

-- name: ListWindowPoints :many
SELECT at_ns, used_percent, reset_ns FROM quota_report_windows
WHERE instance = $1 AND auth_index = $2 AND limit_key = 'unified' AND seconds = sqlc.arg('seconds')::bigint
  AND at_ns >= sqlc.arg('from_ns')::bigint AND at_ns < sqlc.arg('to_ns')::bigint
ORDER BY at_ns, report_id;

-- name: ListWindowPointsBucketed :many
-- The last point of each equal-width bucket of [from_ns, to_ns).
SELECT at_ns, used_percent, reset_ns FROM (
    SELECT DISTINCT ON ((at_ns - sqlc.arg('from_ns')::bigint) / sqlc.arg('width_ns')::bigint)
           at_ns, used_percent, reset_ns, report_id
    FROM quota_report_windows
    WHERE instance = $1 AND auth_index = $2 AND limit_key = 'unified' AND seconds = sqlc.arg('seconds')::bigint
      AND at_ns >= sqlc.arg('from_ns')::bigint AND at_ns < sqlc.arg('to_ns')::bigint
    ORDER BY (at_ns - sqlc.arg('from_ns')::bigint) / sqlc.arg('width_ns')::bigint, at_ns DESC, report_id DESC
) last_points
ORDER BY at_ns, report_id;

-- name: CostByBucket :many
-- Cost of complete-accounting requests per UTC bucket of request start.
SELECT date_trunc(sqlc.arg('bucket')::text, requested_at, 'UTC')::timestamptz AS bucket,
       COALESCE(sum(cost_usd), 0)::double precision AS cost_usd, count(*)::bigint AS requests
FROM requests
WHERE instance = $1 AND auth_index = $2 AND accounting = 'complete'
  AND requested_ns >= sqlc.arg('from_ns')::bigint AND requested_ns < sqlc.arg('to_ns')::bigint
GROUP BY 1
ORDER BY 1;
