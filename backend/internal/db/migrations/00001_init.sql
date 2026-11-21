-- +goose Up
-- All objects are created unqualified: the migrator runs with search_path set
-- to the configured bridge schema (BRIDGE_SCHEMA, default "bridge").

CREATE TABLE users (
    id               uuid PRIMARY KEY,
    username         text NOT NULL,
    password_hash    text NOT NULL,
    role             text NOT NULL DEFAULT 'user',
    status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
    balance_micro    bigint NOT NULL DEFAULT 0,
    -- Bumped on every balance change; Redis keeps the newest (balance, version).
    balance_version  bigint NOT NULL DEFAULT 0,
    max_concurrency  integer NOT NULL DEFAULT 3 CHECK (max_concurrency >= 0),
    rpm              integer CHECK (rpm IS NULL OR rpm >= 0),
    note             text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    deleted_at       timestamptz
);
CREATE UNIQUE INDEX users_username_live ON users (lower(username)) WHERE deleted_at IS NULL;

CREATE TABLE accounts (
    id                    uuid PRIMARY KEY,
    name                  text NOT NULL,
    base_url              text NOT NULL,
    cpa_key_enc           bytea NOT NULL,
    quota_token_enc       bytea NOT NULL,
    enabled               boolean NOT NULL DEFAULT true,
    watermark_5h_percent  double precision CHECK (watermark_5h_percent IS NULL OR (watermark_5h_percent >= 0 AND watermark_5h_percent <= 100)),
    max_concurrency       integer CHECK (max_concurrency IS NULL OR max_concurrency >= 0),
    note                  text NOT NULL DEFAULT '',
    -- Filled from the cpa-quota /quota response, never by hand.
    instance              text NOT NULL DEFAULT '',
    subscription          text NOT NULL DEFAULT '',
    tier                  text NOT NULL DEFAULT '',
    auth_index            text NOT NULL DEFAULT '',
    provider              text NOT NULL DEFAULT '',
    -- 'error' is sticky until an admin edits, re-tests or re-enables the account.
    error_status          boolean NOT NULL DEFAULT false,
    error_reason          text NOT NULL DEFAULT '',
    error_at              timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    deleted_at            timestamptz
);
CREATE UNIQUE INDEX accounts_name_live ON accounts (lower(name)) WHERE deleted_at IS NULL;

CREATE TABLE plans (
    id                  uuid PRIMARY KEY,
    name                text NOT NULL,
    description         text NOT NULL DEFAULT '',
    subscription        text NOT NULL CHECK (subscription IN ('claude', 'chatgpt')),
    weekly_quota_micro  bigint NOT NULL CHECK (weekly_quota_micro >= 0),
    price_micro         bigint NOT NULL CHECK (price_micro >= 0),
    duration_value      integer NOT NULL CHECK (duration_value > 0),
    duration_unit       text NOT NULL CHECK (duration_unit IN ('day', 'week', 'month')),
    allowed_models      text[] NOT NULL DEFAULT '{}',
    on_sale             boolean NOT NULL DEFAULT true,
    sort_order          integer NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz
);

CREATE TABLE plan_subscriptions (
    id                  uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES users (id),
    plan_id             uuid NOT NULL REFERENCES plans (id),
    plan_name           text NOT NULL,
    subscription        text NOT NULL,
    weekly_quota_micro  bigint NOT NULL,
    allowed_models      text[] NOT NULL DEFAULT '{}',
    duration_value      integer NOT NULL,
    duration_unit       text NOT NULL,
    price_micro         bigint NOT NULL,
    account_id          uuid REFERENCES accounts (id),
    status              text NOT NULL CHECK (status IN ('pending_assignment', 'active', 'expired', 'cancelled')),
    activated_at        timestamptz,
    expires_at          timestamptz,
    auto_renew          boolean NOT NULL DEFAULT false,
    cancelled_at        timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX plan_subscriptions_user ON plan_subscriptions (user_id, created_at DESC);
CREATE INDEX plan_subscriptions_account ON plan_subscriptions (account_id) WHERE status IN ('active', 'pending_assignment');
CREATE INDEX plan_subscriptions_status ON plan_subscriptions (status, expires_at);

CREATE TABLE plan_usage_periods (
    subscription_id      uuid NOT NULL REFERENCES plan_subscriptions (id),
    period_start         timestamptz NOT NULL,
    period_end           timestamptz NOT NULL,
    quota_micro          bigint NOT NULL,
    used_billed_micro    bigint NOT NULL DEFAULT 0,
    used_upstream_micro  bigint NOT NULL DEFAULT 0,
    version              bigint NOT NULL DEFAULT 0,
    updated_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (subscription_id, period_start)
);

CREATE TABLE api_keys (
    id               uuid PRIMARY KEY,
    user_id          uuid NOT NULL REFERENCES users (id),
    name             text NOT NULL DEFAULT '',
    key_hash         bytea NOT NULL,
    key_enc          bytea NOT NULL,
    key_prefix       text NOT NULL,
    tiers            text[] NOT NULL,
    subscription_id  uuid REFERENCES plan_subscriptions (id),
    max_concurrency  integer CHECK (max_concurrency IS NULL OR max_concurrency >= 0),
    rpm              integer CHECK (rpm IS NULL OR rpm >= 0),
    enabled          boolean NOT NULL DEFAULT true,
    note             text NOT NULL DEFAULT '',
    last_used_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    deleted_at       timestamptz
);
CREATE UNIQUE INDEX api_keys_hash ON api_keys (key_hash);
CREATE INDEX api_keys_user ON api_keys (user_id, created_at DESC) WHERE deleted_at IS NULL;

CREATE TABLE ledger_entries (
    id                   uuid PRIMARY KEY,
    user_id              uuid NOT NULL REFERENCES users (id),
    type                 text NOT NULL CHECK (type IN ('admin_topup', 'admin_adjust', 'plan_purchase', 'plan_renewal', 'api_charge', 'refund')),
    amount_micro         bigint NOT NULL,
    balance_after_micro  bigint NOT NULL,
    ref_type             text NOT NULL DEFAULT '',
    ref_id               text NOT NULL DEFAULT '',
    idempotency_key      text NOT NULL,
    note                 text NOT NULL DEFAULT '',
    operator_id          uuid,
    created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ledger_entries_idem ON ledger_entries (idempotency_key);
CREATE INDEX ledger_entries_user ON ledger_entries (user_id, created_at DESC, id DESC);
CREATE INDEX ledger_entries_created ON ledger_entries (created_at DESC, id DESC);

-- +goose StatementBegin
CREATE FUNCTION ledger_entries_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'ledger_entries is append-only';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER ledger_entries_no_update BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_entries_append_only();

-- Monthly partitions are created by the application (current month plus the
-- next ones) at startup and by a daily job.
CREATE TABLE request_logs (
    request_id           uuid NOT NULL,
    created_at           timestamptz NOT NULL,
    user_id              uuid,
    key_id               uuid,
    account_id           uuid,
    tier                 text NOT NULL DEFAULT '',
    subscription_id      uuid,
    endpoint             text NOT NULL,
    model_raw            text NOT NULL DEFAULT '',
    model                text NOT NULL DEFAULT '',
    stream               boolean NOT NULL DEFAULT false,
    status_code          integer NOT NULL,
    error_type           text NOT NULL DEFAULT '',
    input_tokens         bigint NOT NULL DEFAULT 0,
    cache_read_tokens    bigint NOT NULL DEFAULT 0,
    cache_write_tokens   bigint NOT NULL DEFAULT 0,
    output_tokens        bigint NOT NULL DEFAULT 0,
    reasoning_tokens     bigint NOT NULL DEFAULT 0,
    upstream_cost_micro  bigint NOT NULL DEFAULT 0,
    billed_micro         bigint NOT NULL DEFAULT 0,
    tier_multiplier      double precision NOT NULL DEFAULT 1,
    model_multiplier     double precision NOT NULL DEFAULT 1,
    unpriced             boolean NOT NULL DEFAULT false,
    usage_missing        boolean NOT NULL DEFAULT false,
    latency_ms           integer NOT NULL DEFAULT 0,
    first_byte_ms        integer NOT NULL DEFAULT 0,
    retries              integer NOT NULL DEFAULT 0,
    session_hash         text NOT NULL DEFAULT '',
    client_ip            text NOT NULL DEFAULT '',
    user_agent           text NOT NULL DEFAULT '',
    PRIMARY KEY (created_at, request_id)
) PARTITION BY RANGE (created_at);
CREATE INDEX request_logs_user ON request_logs (user_id, created_at DESC, request_id DESC);
CREATE INDEX request_logs_created ON request_logs (created_at DESC, request_id DESC);

CREATE TABLE usage_hourly (
    hour                 timestamptz NOT NULL,
    user_id              uuid NOT NULL,
    key_id               uuid NOT NULL,
    account_id           uuid NOT NULL,
    model                text NOT NULL,
    tier                 text NOT NULL,
    requests             bigint NOT NULL DEFAULT 0,
    errors               bigint NOT NULL DEFAULT 0,
    input_tokens         bigint NOT NULL DEFAULT 0,
    cache_read_tokens    bigint NOT NULL DEFAULT 0,
    cache_write_tokens   bigint NOT NULL DEFAULT 0,
    output_tokens        bigint NOT NULL DEFAULT 0,
    reasoning_tokens     bigint NOT NULL DEFAULT 0,
    upstream_cost_micro  bigint NOT NULL DEFAULT 0,
    billed_micro         bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (hour, user_id, key_id, account_id, model, tier)
);
CREATE INDEX usage_hourly_user ON usage_hourly (user_id, hour);

CREATE TABLE safeguard_events (
    id           uuid PRIMARY KEY,
    request_id   uuid NOT NULL,
    user_id      uuid,
    key_id       uuid,
    account_id   uuid,
    model        text NOT NULL DEFAULT '',
    category     text NOT NULL DEFAULT '',
    reason       text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL
);
CREATE UNIQUE INDEX safeguard_events_request ON safeguard_events (request_id);
CREATE INDEX safeguard_events_created ON safeguard_events (created_at DESC, id DESC);

CREATE TABLE audit_logs (
    id           uuid PRIMARY KEY,
    operator_id  uuid,
    action       text NOT NULL,
    target_type  text NOT NULL,
    target_id    text NOT NULL DEFAULT '',
    diff         jsonb NOT NULL DEFAULT '{}',
    ip           text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_created ON audit_logs (created_at DESC, id DESC);

CREATE TABLE settings (
    key         text PRIMARY KEY,
    value       jsonb NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid
);

CREATE TABLE price_tables (
    id          smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    source_url  text NOT NULL,
    body        bytea NOT NULL,
    models      integer NOT NULL,
    fetched_at  timestamptz NOT NULL
);

-- Transactional outbox: rows are written in the same transaction as the
-- change they announce and are delivered to Redis (cache invalidation,
-- balance / quota resync, settings reload) afterwards.
CREATE TABLE outbox (
    id            bigserial PRIMARY KEY,
    kind          text NOT NULL,
    payload       jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    available_at  timestamptz NOT NULL DEFAULT now(),
    attempts      integer NOT NULL DEFAULT 0,
    last_error    text NOT NULL DEFAULT ''
);
CREATE INDEX outbox_available ON outbox (available_at, id);

CREATE TABLE reconcile_runs (
    id                   uuid PRIMARY KEY,
    started_at           timestamptz NOT NULL,
    finished_at          timestamptz NOT NULL,
    ok                   boolean NOT NULL,
    balance_mismatches   integer NOT NULL,
    charge_diff_micro    bigint NOT NULL,
    detail               jsonb NOT NULL DEFAULT '{}'
);

-- +goose Down
DROP TABLE reconcile_runs;
DROP TABLE outbox;
DROP TABLE price_tables;
DROP TABLE settings;
DROP TABLE audit_logs;
DROP TABLE safeguard_events;
DROP TABLE usage_hourly;
DROP TABLE request_logs;
DROP TABLE ledger_entries;
DROP FUNCTION ledger_entries_append_only();
DROP TABLE api_keys;
DROP TABLE plan_usage_periods;
DROP TABLE plan_subscriptions;
DROP TABLE plans;
DROP TABLE accounts;
DROP TABLE users;
