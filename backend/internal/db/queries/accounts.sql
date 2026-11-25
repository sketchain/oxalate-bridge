-- name: CreateAccount :one
INSERT INTO accounts (id, name, base_url, cpa_key_enc, quota_token_enc, enabled, watermark_5h_percent, max_concurrency, note,
                      instance, tier, auth_index)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = $1 AND deleted_at IS NULL;

-- name: ListAccounts :many
SELECT * FROM accounts WHERE deleted_at IS NULL ORDER BY created_at, id;

-- name: UpdateAccount :one
UPDATE accounts SET name = $2, base_url = $3, cpa_key_enc = $4, quota_token_enc = $5, enabled = $6, watermark_5h_percent = $7,
                    max_concurrency = $8, note = $9, instance = $10, tier = $11, auth_index = $12,
                    error_status = false, error_reason = '', error_at = NULL, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SetAccountEnabled :one
UPDATE accounts SET enabled = $2, error_status = CASE WHEN $2 THEN false ELSE error_status END,
                    error_reason = CASE WHEN $2 THEN '' ELSE error_reason END, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteAccount :exec
UPDATE accounts SET deleted_at = now(), enabled = false, updated_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: SetAccountError :exec
UPDATE accounts SET error_status = true, error_reason = $2, error_at = now(), updated_at = now() WHERE id = $1;

-- name: ClearAccountError :exec
UPDATE accounts SET error_status = false, error_reason = '', error_at = NULL, updated_at = now() WHERE id = $1;

-- name: UpdateAccountQuotaInfo :exec
UPDATE accounts SET instance = $2, tier = $3, auth_index = $4, updated_at = now()
WHERE id = $1 AND (instance, tier, auth_index) IS DISTINCT FROM ($2, $3, $4);

-- name: LockAccountForUpdate :one
-- Serializes deleting an account against binding subscriptions to it.
SELECT id FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: LockAccountForShare :one
-- Taken by subscription binds inside their transaction: fails once the
-- account is deleted, and blocks a concurrent delete until the bind commits.
SELECT id FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR SHARE;
