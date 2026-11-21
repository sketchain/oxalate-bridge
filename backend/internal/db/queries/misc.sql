-- name: InsertSafeguardEvent :exec
INSERT INTO safeguard_events (id, request_id, user_id, key_id, account_id, model, category, reason, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (request_id) DO NOTHING;

-- name: ListSafeguardEvents :many
SELECT * FROM safeguard_events
WHERE (sqlc.narg('user_id')::uuid IS NULL OR user_id = sqlc.narg('user_id')::uuid)
  AND (sqlc.narg('cursor_at')::timestamptz IS NULL OR (created_at, id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT $1;

-- name: InsertAuditLog :exec
INSERT INTO audit_logs (id, operator_id, action, target_type, target_id, diff, ip)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListAuditLogs :many
SELECT * FROM audit_logs
WHERE (sqlc.narg('operator_id')::uuid IS NULL OR operator_id = sqlc.narg('operator_id')::uuid)
  AND (sqlc.narg('target_type')::text IS NULL OR target_type = sqlc.narg('target_type')::text)
  AND (sqlc.narg('cursor_at')::timestamptz IS NULL OR (created_at, id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT $1;

-- name: ListSettings :many
SELECT * FROM settings;

-- name: UpsertSetting :exec
INSERT INTO settings (key, value, updated_by) VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now();

-- name: UpsertPriceTable :exec
INSERT INTO price_tables (id, source_url, body, models, fetched_at) VALUES (1, $1, $2, $3, $4)
ON CONFLICT (id) DO UPDATE SET source_url = EXCLUDED.source_url, body = EXCLUDED.body, models = EXCLUDED.models, fetched_at = EXCLUDED.fetched_at;

-- name: GetPriceTable :one
SELECT * FROM price_tables WHERE id = 1;

-- name: InsertOutbox :exec
INSERT INTO outbox (kind, payload) VALUES ($1, $2);

-- name: ClaimOutbox :many
SELECT * FROM outbox WHERE available_at <= now() ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED;

-- name: DeleteOutbox :exec
DELETE FROM outbox WHERE id = ANY($1::bigint[]);

-- name: FailOutbox :exec
UPDATE outbox SET attempts = attempts + 1, last_error = $2,
                  available_at = now() + make_interval(secs => LEAST(60, power(2, attempts)::int))
WHERE id = $1;

-- name: InsertReconcileRun :exec
INSERT INTO reconcile_runs (id, started_at, finished_at, ok, balance_mismatches, charge_diff_micro, detail)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: LastReconcileRun :one
SELECT * FROM reconcile_runs ORDER BY started_at DESC LIMIT 1;
