-- name: AddPeriodUsage :one
INSERT INTO plan_usage_periods (subscription_id, period_start, period_end, quota_micro, used_billed_micro, used_upstream_micro, version)
VALUES ($1, $2, $3, $4, $5, $6, 1)
ON CONFLICT (subscription_id, period_start) DO UPDATE
SET used_billed_micro = plan_usage_periods.used_billed_micro + EXCLUDED.used_billed_micro,
    used_upstream_micro = plan_usage_periods.used_upstream_micro + EXCLUDED.used_upstream_micro,
    period_end = GREATEST(plan_usage_periods.period_end, EXCLUDED.period_end),
    version = plan_usage_periods.version + 1,
    updated_at = now()
RETURNING (quota_micro - used_billed_micro)::bigint AS base, version;

-- name: GetPeriod :one
SELECT * FROM plan_usage_periods WHERE subscription_id = $1 AND period_start = $2;

-- name: ListPeriodsBySubscription :many
SELECT * FROM plan_usage_periods WHERE subscription_id = $1 ORDER BY period_start DESC LIMIT $2;
