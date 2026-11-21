-- name: CreatePlan :one
INSERT INTO plans (id, name, description, subscription, weekly_quota_micro, price_micro, duration_value, duration_unit, allowed_models, on_sale, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetPlan :one
SELECT * FROM plans WHERE id = $1 AND deleted_at IS NULL;

-- name: ListPlans :many
SELECT * FROM plans WHERE deleted_at IS NULL ORDER BY sort_order, created_at;

-- name: ListPlansOnSale :many
SELECT * FROM plans WHERE deleted_at IS NULL AND on_sale ORDER BY sort_order, created_at;

-- name: UpdatePlan :one
UPDATE plans SET name = $2, description = $3, subscription = $4, weekly_quota_micro = $5, price_micro = $6, duration_value = $7,
                 duration_unit = $8, allowed_models = $9, on_sale = $10, sort_order = $11, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeletePlan :exec
UPDATE plans SET deleted_at = now(), on_sale = false, updated_at = now() WHERE id = $1 AND deleted_at IS NULL;
