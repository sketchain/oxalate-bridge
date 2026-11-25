-- +goose Up
-- v0.6.0: cpa-quota v0.1.0 no longer reports a subscription or a provider
-- (it only serves Claude accounts), so the bridge stops storing them.
ALTER TABLE accounts DROP CONSTRAINT accounts_subscription_claude;
ALTER TABLE accounts DROP COLUMN subscription;
ALTER TABLE accounts DROP COLUMN provider;

-- +goose Down
ALTER TABLE accounts ADD COLUMN provider text NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN subscription text NOT NULL DEFAULT 'claude';
ALTER TABLE accounts ALTER COLUMN subscription DROP DEFAULT;
ALTER TABLE accounts ADD CONSTRAINT accounts_subscription_claude
    CHECK (subscription = 'claude' OR deleted_at IS NOT NULL);
