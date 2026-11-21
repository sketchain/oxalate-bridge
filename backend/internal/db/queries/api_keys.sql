-- name: CreateAPIKey :one
INSERT INTO api_keys (id, user_id, name, key_hash, key_enc, key_prefix, tiers, subscription_id, max_concurrency, rpm, enabled, note)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: GetAPIKey :one
SELECT * FROM api_keys WHERE id = $1 AND deleted_at IS NULL;

-- name: ListAPIKeysByUser :many
SELECT * FROM api_keys WHERE user_id = $1 AND deleted_at IS NULL ORDER BY created_at DESC, id DESC;

-- name: UpdateAPIKey :one
UPDATE api_keys SET name = $2, tiers = $3, subscription_id = $4, max_concurrency = $5, rpm = $6, enabled = $7, note = $8, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteAPIKey :exec
UPDATE api_keys SET deleted_at = now(), enabled = false, updated_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: ListAPIKeyHashesByUser :many
SELECT key_hash FROM api_keys WHERE user_id = $1;

-- name: ListAPIKeyHashesBySubscription :many
SELECT key_hash FROM api_keys WHERE subscription_id = $1;

-- name: GetAuthByKeyHash :one
SELECT k.id AS key_id, k.user_id, k.tiers, k.subscription_id, k.max_concurrency AS key_max_concurrency, k.rpm AS key_rpm,
       k.enabled AS key_enabled, (k.deleted_at IS NOT NULL)::boolean AS key_deleted,
       u.role, u.status AS user_status, u.max_concurrency AS user_max_concurrency, u.rpm AS user_rpm
FROM api_keys k JOIN users u ON u.id = k.user_id
WHERE k.key_hash = $1;

-- name: TouchAPIKeys :exec
UPDATE api_keys SET last_used_at = GREATEST(COALESCE(last_used_at, '-infinity'::timestamptz), $2::timestamptz)
WHERE id = ANY($1::uuid[]);
