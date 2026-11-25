-- Safeguard protection: per-user trigger allowance, reset cards, appeals,
-- stored request bodies and the site-wide prompt blocklist.

-- name: GetUserSafeguardForUpdate :one
SELECT id, safeguard_allowance, safeguard_used, safeguard_reset_cards, safeguard_locked_at
FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: GetUserSafeguard :one
SELECT id, username, safeguard_allowance, safeguard_used, safeguard_reset_cards, safeguard_locked_at
FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: SetUserSafeguard :one
-- locked_at is kept while the count stays at or above the allowance and
-- cleared once it drops below; it is set when the count first reaches it.
UPDATE users SET safeguard_allowance = sqlc.narg('allowance'), safeguard_used = sqlc.arg('used'),
    safeguard_reset_cards = sqlc.arg('reset_cards'),
    safeguard_locked_at = CASE WHEN sqlc.arg('used') >= COALESCE(sqlc.narg('allowance'), sqlc.arg('default_allowance')::integer)
                               THEN COALESCE(safeguard_locked_at, now()) END,
    updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING id, username, safeguard_allowance, safeguard_used, safeguard_reset_cards, safeguard_locked_at;

-- name: InsertSafeguardEvent :one
-- Returns no row for a request already recorded.
INSERT INTO safeguard_events (id, request_id, user_id, key_id, account_id, model, category, reason, created_at, status_code, stream,
                              counted, prompt_hash, prompt, body_gz, body_size)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
ON CONFLICT (request_id) DO NOTHING
RETURNING id;

-- name: ListSafeguardEvents :many
SELECT e.id, e.request_id, e.user_id, e.key_id, e.account_id, e.model, e.category, e.reason, e.created_at, e.status_code,
       e.stream, e.counted, e.prompt_hash, left(e.prompt, 500)::text AS prompt, (e.body_gz IS NOT NULL)::boolean AS has_body,
       e.body_size, COALESCE(u.username, '')::text AS username, COALESCE(k.name, '')::text AS key_name,
       COALESCE(k.key_prefix, '')::text AS key_prefix, COALESCE(a.name, '')::text AS account_name
FROM safeguard_events e
LEFT JOIN users u ON u.id = e.user_id
LEFT JOIN api_keys k ON k.id = e.key_id
LEFT JOIN accounts a ON a.id = e.account_id
WHERE (sqlc.narg('user_id')::uuid IS NULL OR e.user_id = sqlc.narg('user_id')::uuid)
  AND (sqlc.narg('prompt_hash')::text IS NULL OR e.prompt_hash = sqlc.narg('prompt_hash')::text)
  AND (sqlc.narg('cursor_at')::timestamptz IS NULL OR (e.created_at, e.id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY e.created_at DESC, e.id DESC
LIMIT $1;

-- name: GetSafeguardEvent :one
SELECT e.id, e.request_id, e.user_id, e.key_id, e.account_id, e.model, e.category, e.reason, e.created_at, e.status_code,
       e.stream, e.counted, e.prompt_hash, e.prompt, (e.body_gz IS NOT NULL)::boolean AS has_body, e.body_size,
       COALESCE(u.username, '')::text AS username, COALESCE(k.name, '')::text AS key_name,
       COALESCE(k.key_prefix, '')::text AS key_prefix, COALESCE(a.name, '')::text AS account_name
FROM safeguard_events e
LEFT JOIN users u ON u.id = e.user_id
LEFT JOIN api_keys k ON k.id = e.key_id
LEFT JOIN accounts a ON a.id = e.account_id
WHERE e.id = $1;

-- name: GetSafeguardEventBody :one
SELECT body_gz, body_size FROM safeguard_events WHERE id = $1;

-- name: CountUserSafeguardEvents :one
SELECT count(*)::bigint FROM safeguard_events WHERE user_id = $1;

-- name: GetSafeguardBlockForUpdate :one
SELECT * FROM safeguard_blocks WHERE hash = $1 FOR UPDATE;

-- name: UpsertSafeguardBlock :exec
-- A revoked entry triggered again goes back on the list (as a new entry).
INSERT INTO safeguard_blocks (hash, prompt, first_event_id, first_user_id, created_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (hash) DO UPDATE SET prompt = EXCLUDED.prompt, first_event_id = EXCLUDED.first_event_id,
    first_user_id = EXCLUDED.first_user_id, created_at = EXCLUDED.created_at, hits = 0, last_hit_at = NULL,
    revoked_at = NULL, revoked_by = NULL
WHERE safeguard_blocks.revoked_at IS NOT NULL;

-- name: ListActiveSafeguardBlockHashes :many
SELECT hash FROM safeguard_blocks WHERE revoked_at IS NULL;

-- name: ListSafeguardBlocks :many
SELECT b.hash, left(b.prompt, 500)::text AS prompt, b.first_event_id, b.first_user_id, b.created_at, b.hits, b.last_hit_at,
       b.revoked_at, b.revoked_by, COALESCE(u.username, '')::text AS first_username,
       COALESCE(r.username, '')::text AS revoked_by_username
FROM safeguard_blocks b
LEFT JOIN users u ON u.id = b.first_user_id
LEFT JOIN users r ON r.id = b.revoked_by
WHERE (sqlc.arg('status')::text = 'all' OR (sqlc.arg('status')::text = 'active') = (b.revoked_at IS NULL))
  AND (sqlc.narg('search')::text IS NULL OR b.prompt ILIKE '%' || sqlc.narg('search')::text || '%' OR b.hash = sqlc.narg('search')::text)
  AND (sqlc.narg('cursor_at')::timestamptz IS NULL OR (b.created_at, b.hash) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_hash')::text))
ORDER BY b.created_at DESC, b.hash DESC
LIMIT $1;

-- name: GetSafeguardBlock :one
SELECT b.hash, b.prompt, b.first_event_id, b.first_user_id, b.created_at, b.hits, b.last_hit_at, b.revoked_at, b.revoked_by,
       COALESCE(u.username, '')::text AS first_username, COALESCE(r.username, '')::text AS revoked_by_username
FROM safeguard_blocks b
LEFT JOIN users u ON u.id = b.first_user_id
LEFT JOIN users r ON r.id = b.revoked_by
WHERE b.hash = $1;

-- name: RevokeSafeguardBlock :one
UPDATE safeguard_blocks SET revoked_at = now(), revoked_by = $2
WHERE hash = $1 AND revoked_at IS NULL
RETURNING hash;

-- name: InsertSafeguardBlockedRequest :exec
INSERT INTO safeguard_blocked_requests (id, request_id, user_id, key_id, hash, created_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: CountSafeguardBlockHit :exec
UPDATE safeguard_blocks SET hits = hits + 1, last_hit_at = GREATEST(COALESCE(last_hit_at, '-infinity'::timestamptz), sqlc.arg('at')::timestamptz)
WHERE hash = $1;

-- name: CountUserBlockedRequests :one
SELECT count(*)::bigint FROM safeguard_blocked_requests WHERE user_id = $1;

-- name: ListUserBlockedRequests :many
SELECT b.id, b.created_at, b.key_id, COALESCE(k.name, '')::text AS key_name, COALESCE(k.key_prefix, '')::text AS key_prefix
FROM safeguard_blocked_requests b LEFT JOIN api_keys k ON k.id = b.key_id
WHERE b.user_id = $1
ORDER BY b.created_at DESC, b.id DESC
LIMIT $2;

-- name: CreateSafeguardAppeal :one
INSERT INTO safeguard_appeals (id, user_id, note) VALUES ($1, $2, $3)
RETURNING *;

-- name: GetSafeguardAppealForUpdate :one
SELECT * FROM safeguard_appeals WHERE id = $1 FOR UPDATE;

-- name: GetLatestSafeguardAppeal :one
SELECT * FROM safeguard_appeals WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: DecideSafeguardAppeal :one
UPDATE safeguard_appeals SET status = $2, reject_reason = $3, decided_by = $4, decided_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: ListSafeguardAppeals :many
SELECT a.*, COALESCE(u.username, '')::text AS username, COALESCE(d.username, '')::text AS decided_by_username,
       u.safeguard_used, u.safeguard_allowance, u.safeguard_reset_cards
FROM safeguard_appeals a
JOIN users u ON u.id = a.user_id
LEFT JOIN users d ON d.id = a.decided_by
WHERE (sqlc.arg('status')::text = 'all' OR a.status = sqlc.arg('status')::text)
  AND (sqlc.narg('user_id')::uuid IS NULL OR a.user_id = sqlc.narg('user_id')::uuid)
  AND (sqlc.narg('cursor_at')::timestamptz IS NULL OR (a.created_at, a.id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY a.created_at DESC, a.id DESC
LIMIT $1;

-- name: CountPendingSafeguardAppeals :one
SELECT count(*)::bigint FROM safeguard_appeals WHERE status = 'pending';

-- name: ListUserSafeguardEvents :many
-- The user's own triggers, without prompt or body.
SELECT id, created_at, model, category, counted
FROM safeguard_events WHERE user_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: GetSafeguardAppealView :one
SELECT a.*, COALESCE(u.username, '')::text AS username, COALESCE(d.username, '')::text AS decided_by_username,
       u.safeguard_used, u.safeguard_allowance, u.safeguard_reset_cards
FROM safeguard_appeals a
JOIN users u ON u.id = a.user_id
LEFT JOIN users d ON d.id = a.decided_by
WHERE a.id = $1;
