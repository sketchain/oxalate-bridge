-- +goose Up
-- v0.2.0: the bridge only serves Claude subscriptions.
--
-- Rows that are already gone (soft-deleted accounts and plans, expired or
-- cancelled subscriptions) keep whatever they recorded; every live row must
-- be claude. The migration stops with a clear message instead of silently
-- rewriting data when a live non-claude row exists.

-- +goose StatementBegin
DO $$
DECLARE
    n_accounts integer;
    n_plans integer;
    n_subs integer;
BEGIN
    SELECT count(*) INTO n_accounts FROM accounts WHERE deleted_at IS NULL AND subscription <> 'claude';
    SELECT count(*) INTO n_plans FROM plans WHERE deleted_at IS NULL AND subscription <> 'claude';
    SELECT count(*) INTO n_subs FROM plan_subscriptions
        WHERE status IN ('active', 'pending_assignment') AND subscription <> 'claude';
    IF n_accounts + n_plans + n_subs > 0 THEN
        RAISE EXCEPTION 'oxalate-bridge v0.2.0 only supports claude: delete or cancel % non-claude account(s), % plan(s) and % live subscription(s) with v0.1.x first',
            n_accounts, n_plans, n_subs;
    END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE accounts ALTER COLUMN subscription DROP DEFAULT;
ALTER TABLE accounts ADD CONSTRAINT accounts_subscription_claude
    CHECK (subscription = 'claude' OR deleted_at IS NOT NULL);

ALTER TABLE plans DROP CONSTRAINT plans_subscription_check;
ALTER TABLE plans ADD CONSTRAINT plans_subscription_claude
    CHECK (subscription = 'claude' OR deleted_at IS NOT NULL);

ALTER TABLE plan_subscriptions ADD CONSTRAINT plan_subscriptions_subscription_claude
    CHECK (subscription = 'claude' OR status IN ('expired', 'cancelled'));

-- +goose Down
ALTER TABLE plan_subscriptions DROP CONSTRAINT plan_subscriptions_subscription_claude;
ALTER TABLE plans DROP CONSTRAINT plans_subscription_claude;
ALTER TABLE plans ADD CONSTRAINT plans_subscription_check CHECK (subscription IN ('claude', 'chatgpt'));
ALTER TABLE accounts DROP CONSTRAINT accounts_subscription_claude;
ALTER TABLE accounts ALTER COLUMN subscription SET DEFAULT '';
