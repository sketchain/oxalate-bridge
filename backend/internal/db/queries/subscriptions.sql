-- name: CreateSubscription :one
INSERT INTO plan_subscriptions (id, user_id, plan_id, plan_name, subscription, weekly_quota_micro, allowed_models, duration_value,
                                duration_unit, price_micro, account_id, status, activated_at, expires_at, auto_renew)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING *;

-- name: GetSubscription :one
SELECT * FROM plan_subscriptions WHERE id = $1;

-- name: GetSubscriptionForUpdate :one
SELECT * FROM plan_subscriptions WHERE id = $1 FOR UPDATE;

-- name: ListSubscriptionsByUser :many
SELECT * FROM plan_subscriptions WHERE user_id = $1 ORDER BY created_at DESC, id DESC;

-- name: ListSubscriptions :many
SELECT * FROM plan_subscriptions
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('user_id')::uuid IS NULL OR user_id = sqlc.narg('user_id')::uuid)
  AND (sqlc.narg('account_id')::uuid IS NULL OR account_id = sqlc.narg('account_id')::uuid)
  AND (sqlc.narg('cursor_at')::timestamptz IS NULL OR (created_at, id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT $1;

-- name: UpdateSubscriptionState :one
UPDATE plan_subscriptions SET account_id = $2, status = $3, activated_at = $4, expires_at = $5, auto_renew = $6,
                              cancelled_at = $7, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ListLiveSubscriptions :many
SELECT * FROM plan_subscriptions WHERE status IN ('active', 'pending_assignment');

-- name: ListDueRenewals :many
SELECT id FROM plan_subscriptions
WHERE status = 'active' AND auto_renew AND expires_at <= $1
ORDER BY expires_at
LIMIT 200;

-- name: ListExpiredActive :many
SELECT id FROM plan_subscriptions WHERE status = 'active' AND expires_at <= now() LIMIT 500;

-- name: SumQuotaByAccount :one
SELECT COALESCE(sum(weekly_quota_micro), 0)::bigint FROM plan_subscriptions
WHERE account_id = $1 AND status IN ('active', 'pending_assignment') AND id <> $2;

-- name: ListBlockingSubscriptionsByAccount :many
-- Subscriptions that prevent deleting their account.
SELECT s.id, s.user_id, u.username, s.status, s.plan_name
FROM plan_subscriptions s JOIN users u ON u.id = s.user_id
WHERE s.account_id = $1 AND s.status IN ('active', 'pending_assignment')
ORDER BY s.created_at, s.id;
