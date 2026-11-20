-- name: InsertRequestLog :one
INSERT INTO request_logs (request_id, created_at, user_id, key_id, account_id, tier, subscription_id, endpoint, model_raw, model, stream,
                          status_code, error_type, input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, reasoning_tokens,
                          upstream_cost_micro, billed_micro, tier_multiplier, model_multiplier, unpriced, usage_missing, latency_ms,
                          first_byte_ms, retries, session_hash, client_ip, user_agent)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30)
ON CONFLICT (created_at, request_id) DO NOTHING
RETURNING request_id;

-- name: ListRequestLogs :many
SELECT * FROM request_logs
WHERE (sqlc.narg('user_id')::uuid IS NULL OR user_id = sqlc.narg('user_id')::uuid)
  AND (sqlc.narg('key_id')::uuid IS NULL OR key_id = sqlc.narg('key_id')::uuid)
  AND (sqlc.narg('account_id')::uuid IS NULL OR account_id = sqlc.narg('account_id')::uuid)
  AND (sqlc.narg('model')::text IS NULL OR model = sqlc.narg('model')::text)
  AND (sqlc.narg('tier')::text IS NULL OR tier = sqlc.narg('tier')::text)
  AND (sqlc.narg('from_at')::timestamptz IS NULL OR created_at >= sqlc.narg('from_at')::timestamptz)
  AND (sqlc.narg('to_at')::timestamptz IS NULL OR created_at < sqlc.narg('to_at')::timestamptz)
  AND (sqlc.narg('cursor_at')::timestamptz IS NULL OR (created_at, request_id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY created_at DESC, request_id DESC
LIMIT $1;

-- name: SumBilledBalanceTiers :one
SELECT COALESCE(sum(billed_micro), 0)::bigint FROM request_logs WHERE tier IN ('standard', 'force');
