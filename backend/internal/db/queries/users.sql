-- name: CreateUser :one
INSERT INTO users (id, username, password_hash, role, status, max_concurrency, rpm, note)
VALUES ($1, $2, $3, $4, 'active', $5, $6, $7)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: GetUserForUpdate :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE lower(username) = lower($1) AND deleted_at IS NULL;

-- name: ListUsers :many
SELECT * FROM users
WHERE deleted_at IS NULL
  AND (sqlc.narg('search')::text IS NULL OR username ILIKE '%' || sqlc.narg('search')::text || '%')
  AND (sqlc.narg('cursor_at')::timestamptz IS NULL OR (created_at, id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT $1;

-- name: UpdateUser :one
UPDATE users SET role = $2, status = $3, max_concurrency = $4, rpm = $5, note = $6, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: SoftDeleteUser :exec
UPDATE users SET status = 'deleted', deleted_at = now(), updated_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: CountAdmins :one
SELECT count(*) FROM users WHERE role = 'admin' AND deleted_at IS NULL;

-- name: SetUserBalance :one
UPDATE users SET balance_micro = $2, balance_version = balance_version + 1, updated_at = now() WHERE id = $1
RETURNING balance_version;

-- name: GetUserBalance :one
SELECT balance_micro, balance_version FROM users WHERE id = $1;

-- name: ListUserBalanceChecks :many
SELECT u.id, u.balance_micro, COALESCE(l.total, 0)::bigint AS ledger_total
FROM users u
LEFT JOIN (SELECT user_id, sum(amount_micro) AS total FROM ledger_entries GROUP BY user_id) l ON l.user_id = u.id
WHERE u.balance_micro <> COALESCE(l.total, 0);

-- name: ListUsernames :many
-- Usernames for display (deleted users included).
SELECT id, username FROM users WHERE id = ANY(sqlc.arg('ids')::uuid[]);
