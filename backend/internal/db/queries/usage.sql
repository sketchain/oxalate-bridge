-- name: AddUsageHourly :exec
INSERT INTO usage_hourly (hour, user_id, key_id, account_id, model, tier, requests, errors, input_tokens, cache_read_tokens,
                          cache_write_tokens, output_tokens, reasoning_tokens, upstream_cost_micro, billed_micro)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (hour, user_id, key_id, account_id, model, tier) DO UPDATE
SET requests = usage_hourly.requests + EXCLUDED.requests,
    errors = usage_hourly.errors + EXCLUDED.errors,
    input_tokens = usage_hourly.input_tokens + EXCLUDED.input_tokens,
    cache_read_tokens = usage_hourly.cache_read_tokens + EXCLUDED.cache_read_tokens,
    cache_write_tokens = usage_hourly.cache_write_tokens + EXCLUDED.cache_write_tokens,
    output_tokens = usage_hourly.output_tokens + EXCLUDED.output_tokens,
    reasoning_tokens = usage_hourly.reasoning_tokens + EXCLUDED.reasoning_tokens,
    upstream_cost_micro = usage_hourly.upstream_cost_micro + EXCLUDED.upstream_cost_micro,
    billed_micro = usage_hourly.billed_micro + EXCLUDED.billed_micro;

-- name: UsageSummary :many
SELECT date_trunc(sqlc.arg('bucket')::text, hour, 'UTC')::timestamptz AS bucket, model, tier,
       sum(requests)::bigint AS requests, sum(errors)::bigint AS errors,
       sum(input_tokens)::bigint AS input_tokens, sum(cache_read_tokens)::bigint AS cache_read_tokens,
       sum(cache_write_tokens)::bigint AS cache_write_tokens, sum(output_tokens)::bigint AS output_tokens,
       sum(reasoning_tokens)::bigint AS reasoning_tokens, sum(upstream_cost_micro)::bigint AS upstream_cost_micro,
       sum(billed_micro)::bigint AS billed_micro
FROM usage_hourly
WHERE hour >= sqlc.arg('from_at')::timestamptz AND hour < sqlc.arg('to_at')::timestamptz
  AND (sqlc.narg('user_id')::uuid IS NULL OR user_id = sqlc.narg('user_id')::uuid)
  AND (sqlc.narg('key_id')::uuid IS NULL OR key_id = sqlc.narg('key_id')::uuid)
  AND (sqlc.narg('account_id')::uuid IS NULL OR account_id = sqlc.narg('account_id')::uuid)
GROUP BY 1, 2, 3
ORDER BY 1, 2, 3;

-- name: AccountCostByBucket :many
-- This bridge's upstream cost of one account per UTC bucket (request start hour).
SELECT date_trunc(sqlc.arg('bucket')::text, hour, 'UTC')::timestamptz AS bucket,
       sum(upstream_cost_micro)::bigint AS upstream_cost_micro, sum(requests)::bigint AS requests
FROM usage_hourly
WHERE account_id = $1 AND hour >= sqlc.arg('from_at')::timestamptz AND hour < sqlc.arg('to_at')::timestamptz
GROUP BY 1
ORDER BY 1;

-- name: KeyDailyUsage :many
-- One key per day (in the given time zone) and tier, from the hourly rollup.
SELECT (date_trunc('day', hour AT TIME ZONE sqlc.arg('tz')::text))::date AS day, tier,
       sum(requests)::bigint AS requests, sum(errors)::bigint AS errors,
       sum(input_tokens)::bigint AS input_tokens, sum(cache_read_tokens)::bigint AS cache_read_tokens,
       sum(cache_write_tokens)::bigint AS cache_write_tokens, sum(output_tokens)::bigint AS output_tokens,
       sum(reasoning_tokens)::bigint AS reasoning_tokens, sum(billed_micro)::bigint AS billed_micro
FROM usage_hourly
WHERE key_id = sqlc.arg('key_id') AND hour >= sqlc.arg('from_at')::timestamptz
GROUP BY 1, 2
ORDER BY 1, 2;

-- name: UserKeyTotals :many
SELECT key_id, sum(requests)::bigint AS requests, sum(errors)::bigint AS errors,
       sum(input_tokens)::bigint AS input_tokens, sum(cache_read_tokens)::bigint AS cache_read_tokens,
       sum(cache_write_tokens)::bigint AS cache_write_tokens, sum(output_tokens)::bigint AS output_tokens,
       sum(reasoning_tokens)::bigint AS reasoning_tokens, sum(billed_micro)::bigint AS billed_micro
FROM usage_hourly
WHERE user_id = sqlc.arg('user_id') AND hour >= sqlc.arg('from_at')::timestamptz AND hour < sqlc.arg('to_at')::timestamptz
GROUP BY key_id
ORDER BY billed_micro DESC, requests DESC;

-- name: UserKeyDaily :many
SELECT key_id, (date_trunc('day', hour AT TIME ZONE sqlc.arg('tz')::text))::date AS day,
       sum(requests)::bigint AS requests, sum(billed_micro)::bigint AS billed_micro
FROM usage_hourly
WHERE user_id = sqlc.arg('user_id') AND key_id = ANY(sqlc.arg('key_ids')::uuid[])
  AND hour >= sqlc.arg('from_at')::timestamptz AND hour < sqlc.arg('to_at')::timestamptz
GROUP BY 1, 2
ORDER BY 2, 1;

-- name: ListAllAPIKeysByUser :many
-- Including deleted keys (usage comparison names them).
SELECT id, name, key_prefix, deleted_at FROM api_keys WHERE user_id = $1;
