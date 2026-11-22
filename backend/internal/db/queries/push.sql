-- name: CreateAdminToken :one
INSERT INTO admin_tokens (id, name, token_hash, token_prefix, scope, created_by, expires_at, ip_allowlist)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetAdminTokenByHash :one
SELECT * FROM admin_tokens WHERE token_hash = $1;

-- name: GetAdminToken :one
SELECT * FROM admin_tokens WHERE id = $1;

-- name: ListAdminTokens :many
SELECT t.*, u.username AS created_by_username
FROM admin_tokens t LEFT JOIN users u ON u.id = t.created_by
ORDER BY t.created_at DESC, t.id DESC;

-- name: RevokeAdminToken :one
UPDATE admin_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: TouchAdminToken :exec
UPDATE admin_tokens SET last_used_at = $2, last_used_ip = $3 WHERE id = $1;

-- name: UpsertPushSubscription :one
-- A browser endpoint belongs to the user that registered it last.
INSERT INTO push_subscriptions (id, user_id, endpoint, p256dh, auth, device_label)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (endpoint) DO UPDATE SET user_id = EXCLUDED.user_id, p256dh = EXCLUDED.p256dh, auth = EXCLUDED.auth,
    device_label = EXCLUDED.device_label, failures = 0
RETURNING *;

-- name: ListPushSubscriptions :many
SELECT * FROM push_subscriptions WHERE user_id = $1 ORDER BY created_at DESC, id DESC;

-- name: GetPushSubscription :one
SELECT * FROM push_subscriptions WHERE id = $1;

-- name: DeletePushSubscription :execrows
DELETE FROM push_subscriptions WHERE id = $1 AND user_id = $2;

-- name: DeletePushSubscriptionByEndpoint :execrows
DELETE FROM push_subscriptions WHERE endpoint = $1 AND (sqlc.narg('user_id')::uuid IS NULL OR user_id = sqlc.narg('user_id')::uuid);

-- name: DeletePushSubscriptionByID :exec
DELETE FROM push_subscriptions WHERE id = $1;

-- name: PushSucceeded :exec
UPDATE push_subscriptions SET last_success_at = now(), failures = 0 WHERE id = $1;

-- name: PushFailed :exec
UPDATE push_subscriptions SET failures = failures + 1 WHERE id = $1;

-- name: ListNotificationPrefs :many
SELECT * FROM notification_prefs WHERE user_id = $1;

-- name: ListNotificationPrefsForEvent :many
SELECT * FROM notification_prefs WHERE event = $1;

-- name: UpsertNotificationPref :exec
INSERT INTO notification_prefs (user_id, event, enabled, threshold_micro)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, event) DO UPDATE SET enabled = EXCLUDED.enabled, threshold_micro = EXCLUDED.threshold_micro, updated_at = now();

-- name: ListSubscriptionsExpiring :many
-- Active subscriptions that expire within the window (plan_expiring).
SELECT id, user_id, plan_name, expires_at, auto_renew FROM plan_subscriptions
WHERE status = 'active' AND expires_at IS NOT NULL AND expires_at > now() AND expires_at <= sqlc.arg('until')::timestamptz;

-- name: ListSafeguardEventsSince :many
SELECT s.id, s.user_id, s.category, s.created_at, COALESCE(u.username, '')::text AS username
FROM safeguard_events s LEFT JOIN users u ON u.id = s.user_id
WHERE s.created_at > sqlc.arg('since')::timestamptz
ORDER BY s.created_at, s.id
LIMIT 100;
