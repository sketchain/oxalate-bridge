-- name: CreateRedeemBatch :one
INSERT INTO redeem_batches (id, prefix, amount_micro, count, expires_at, note, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: InsertRedeemCode :exec
INSERT INTO redeem_codes (id, batch_id, code_hash, code_enc, code_hint) VALUES ($1, $2, $3, $4, $5);

-- name: GetRedeemBatch :one
SELECT * FROM redeem_batches WHERE id = $1;

-- name: ListRedeemBatches :many
SELECT b.*,
       count(c.id) FILTER (WHERE c.status = 'unused')::bigint AS unused,
       count(c.id) FILTER (WHERE c.status = 'used')::bigint AS used,
       count(c.id) FILTER (WHERE c.status = 'void')::bigint AS voided
FROM redeem_batches b LEFT JOIN redeem_codes c ON c.batch_id = b.id
WHERE (sqlc.narg('cursor_at')::timestamptz IS NULL OR (b.created_at, b.id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
GROUP BY b.id
ORDER BY b.created_at DESC, b.id DESC
LIMIT $1;

-- name: ListRedeemCodes :many
SELECT c.*, b.amount_micro, b.expires_at, b.prefix
FROM redeem_codes c JOIN redeem_batches b ON b.id = c.batch_id
WHERE (sqlc.narg('batch_id')::uuid IS NULL OR c.batch_id = sqlc.narg('batch_id')::uuid)
  AND (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
  AND (sqlc.narg('cursor_at')::timestamptz IS NULL OR (c.created_at, c.id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY c.created_at DESC, c.id DESC
LIMIT $1;

-- name: ListRedeemCodesByBatch :many
SELECT * FROM redeem_codes WHERE batch_id = $1 ORDER BY created_at, id;

-- name: GetRedeemCodeByHash :one
SELECT c.*, b.amount_micro, b.expires_at FROM redeem_codes c JOIN redeem_batches b ON b.id = c.batch_id
WHERE c.code_hash = $1;

-- name: GetRedeemCode :one
SELECT * FROM redeem_codes WHERE id = $1;

-- name: UseRedeemCode :one
-- Conditional update: only one transaction can move a code from unused.
UPDATE redeem_codes c SET status = 'used', used_by = $2, used_at = now()
FROM redeem_batches b
WHERE c.id = $1 AND c.batch_id = b.id AND c.status = 'unused' AND (b.expires_at IS NULL OR b.expires_at > now())
RETURNING c.id, b.amount_micro;

-- name: VoidRedeemCode :one
UPDATE redeem_codes SET status = 'void', voided_at = now() WHERE id = $1 AND status = 'unused' RETURNING *;
