-- name: CreateAPIKey :one
INSERT INTO api_keys (id, user_id, name, key_hash, key_enc, key_prefix, tiers, subscription_id, max_concurrency, rpm, enabled, note,
                      quota_limit_micro, quota_scope, limit_5h_micro, limit_1d_micro, limit_7d_micro, expires_at, model_allowlist,
                      ip_allowlist, allow_owner_query)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
RETURNING *;

-- name: GetAPIKey :one
SELECT * FROM api_keys WHERE id = $1 AND deleted_at IS NULL;

-- name: ListAPIKeysByUser :many
SELECT * FROM api_keys WHERE user_id = $1 AND deleted_at IS NULL ORDER BY created_at DESC, id DESC;

-- name: UpdateAPIKey :one
UPDATE api_keys SET name = $2, tiers = $3, subscription_id = $4, max_concurrency = $5, rpm = $6, enabled = $7, note = $8,
    quota_limit_micro = $9, quota_scope = $10, limit_5h_micro = $11, limit_1d_micro = $12, limit_7d_micro = $13, expires_at = $14,
    model_allowlist = $15, ip_allowlist = $16, allow_owner_query = $17, updated_at = now()
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
       k.quota_limit_micro, k.quota_scope, k.limit_5h_micro, k.limit_1d_micro, k.limit_7d_micro, k.expires_at,
       k.model_allowlist, k.ip_allowlist, k.allow_owner_query,
       u.role, u.status AS user_status, u.max_concurrency AS user_max_concurrency, u.rpm AS user_rpm
FROM api_keys k JOIN users u ON u.id = k.user_id
WHERE k.key_hash = $1;

-- name: TouchAPIKeys :exec
UPDATE api_keys SET last_used_at = GREATEST(COALESCE(last_used_at, '-infinity'::timestamptz), $2::timestamptz)
WHERE id = ANY($1::uuid[]);

-- name: ResetAPIKeyUsage :one
UPDATE api_keys SET used_all_micro = 0, used_balance_micro = 0, usage_version = usage_version + 1,
    usage_reset_at = now(), updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: GetAPIKeyUsageBase :one
SELECT used_all_micro, used_balance_micro, usage_version, usage_reset_at FROM api_keys WHERE id = $1;

-- name: LockAPIKeyUsage :one
-- Locks the key row so a concurrent reset either commits first (and its
-- reset time filters this batch) or waits for the batch.
SELECT usage_reset_at FROM api_keys WHERE id = $1 FOR UPDATE;

-- name: AddAPIKeyUsage :one
UPDATE api_keys SET used_all_micro = used_all_micro + $2, used_balance_micro = used_balance_micro + $3, usage_version = usage_version + 1
WHERE id = $1
RETURNING used_all_micro, used_balance_micro, usage_version;

-- name: KeyBilledByMinute :many
-- Window rebuild: billed per minute over the last 7 days (all / balance tiers).
SELECT (floor(extract(epoch FROM created_at) / 60))::bigint AS minute,
       sum(billed_micro)::bigint AS all_micro,
       COALESCE(sum(billed_micro) FILTER (WHERE tier IN ('standard', 'force')), 0)::bigint AS balance_micro
FROM request_logs
WHERE key_id = $1 AND created_at >= $2 AND billed_micro <> 0
GROUP BY 1;

-- name: ListRecentKeyLogs :many
SELECT * FROM request_logs WHERE key_id = $1 AND created_at >= $2
ORDER BY created_at DESC, request_id DESC LIMIT $3;
