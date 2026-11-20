-- name: InsertLedgerEntry :one
INSERT INTO ledger_entries (id, user_id, type, amount_micro, balance_after_micro, ref_type, ref_id, idempotency_key, note, operator_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING *;

-- name: ListLedger :many
SELECT * FROM ledger_entries
WHERE (sqlc.narg('user_id')::uuid IS NULL OR user_id = sqlc.narg('user_id')::uuid)
  AND (sqlc.narg('type')::text IS NULL OR type = sqlc.narg('type')::text)
  AND (sqlc.narg('cursor_at')::timestamptz IS NULL OR (created_at, id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT $1;

-- name: SumAPICharges :one
SELECT COALESCE(-sum(amount_micro), 0)::bigint FROM ledger_entries WHERE type = 'api_charge';
