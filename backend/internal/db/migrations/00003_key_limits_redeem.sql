-- +goose Up
-- v0.4.0: API key limits and usage meters, redeem codes, announcements.

ALTER TABLE api_keys
    ADD COLUMN quota_limit_micro  bigint CHECK (quota_limit_micro IS NULL OR quota_limit_micro >= 0),
    ADD COLUMN quota_scope        text NOT NULL DEFAULT 'all' CHECK (quota_scope IN ('all', 'balance')),
    ADD COLUMN limit_5h_micro     bigint CHECK (limit_5h_micro IS NULL OR limit_5h_micro >= 0),
    ADD COLUMN limit_1d_micro     bigint CHECK (limit_1d_micro IS NULL OR limit_1d_micro >= 0),
    ADD COLUMN limit_7d_micro     bigint CHECK (limit_7d_micro IS NULL OR limit_7d_micro >= 0),
    ADD COLUMN expires_at         timestamptz,
    -- NULL = no restriction. Models are stored normalized, IPs as canonical
    -- prefixes (a single address is a /32 or /128).
    ADD COLUMN model_allowlist    text[],
    ADD COLUMN ip_allowlist       text[],
    ADD COLUMN allow_owner_query  boolean NOT NULL DEFAULT false,
    -- Billed amounts counted since the last reset: every charge (all) and the
    -- charges of the balance tiers only (balance). Redis keeps the newest
    -- (all, balance, version) plus the amounts not yet persisted.
    ADD COLUMN used_all_micro     bigint NOT NULL DEFAULT 0,
    ADD COLUMN used_balance_micro bigint NOT NULL DEFAULT 0,
    ADD COLUMN usage_version      bigint NOT NULL DEFAULT 0,
    -- Requests that started before this instant do not count (see reset).
    ADD COLUMN usage_reset_at     timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00';

-- Existing keys start with the usage they already have.
UPDATE api_keys k
SET used_all_micro = s.all_micro, used_balance_micro = s.balance_micro
FROM (
    SELECT key_id, sum(billed_micro)::bigint AS all_micro,
           COALESCE(sum(billed_micro) FILTER (WHERE tier IN ('standard', 'force')), 0)::bigint AS balance_micro
    FROM usage_hourly GROUP BY key_id
) s
WHERE s.key_id = k.id;

CREATE INDEX usage_hourly_key ON usage_hourly (key_id, hour);
CREATE INDEX request_logs_key ON request_logs (key_id, created_at DESC, request_id DESC);

ALTER TABLE ledger_entries DROP CONSTRAINT ledger_entries_type_check;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_type_check
    CHECK (type IN ('admin_topup', 'admin_adjust', 'plan_purchase', 'plan_renewal', 'api_charge', 'refund', 'redeem'));

CREATE TABLE redeem_batches (
    id            uuid PRIMARY KEY,
    prefix        text NOT NULL DEFAULT '',
    amount_micro  bigint NOT NULL CHECK (amount_micro > 0),
    count         integer NOT NULL CHECK (count BETWEEN 1 AND 1000),
    expires_at    timestamptz,
    note          text NOT NULL DEFAULT '',
    created_by    uuid REFERENCES users (id),
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX redeem_batches_created ON redeem_batches (created_at DESC, id DESC);

CREATE TABLE redeem_codes (
    id            uuid PRIMARY KEY,
    batch_id      uuid NOT NULL REFERENCES redeem_batches (id),
    -- sha256 of the normalized code (lookup) and the sealed original (export).
    code_hash     bytea NOT NULL,
    code_enc      bytea NOT NULL,
    code_hint     text NOT NULL,
    status        text NOT NULL DEFAULT 'unused' CHECK (status IN ('unused', 'used', 'void')),
    used_by       uuid REFERENCES users (id),
    used_at       timestamptz,
    voided_at     timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX redeem_codes_hash ON redeem_codes (code_hash);
CREATE INDEX redeem_codes_batch ON redeem_codes (batch_id, created_at, id);
CREATE INDEX redeem_codes_list ON redeem_codes (created_at DESC, id DESC);

CREATE TABLE announcements (
    id          uuid PRIMARY KEY,
    title       text NOT NULL,
    body        text NOT NULL DEFAULT '',
    level       text NOT NULL DEFAULT 'info' CHECK (level IN ('info', 'warning')),
    starts_at   timestamptz,
    ends_at     timestamptz,
    enabled     boolean NOT NULL DEFAULT true,
    created_by  uuid REFERENCES users (id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CHECK (starts_at IS NULL OR ends_at IS NULL OR ends_at > starts_at)
);

CREATE TABLE announcement_dismissals (
    announcement_id  uuid NOT NULL REFERENCES announcements (id) ON DELETE CASCADE,
    user_id          uuid NOT NULL REFERENCES users (id),
    dismissed_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (announcement_id, user_id)
);

-- +goose Down
DROP TABLE announcement_dismissals;
DROP TABLE announcements;
DROP TABLE redeem_codes;
DROP TABLE redeem_batches;
ALTER TABLE ledger_entries DROP CONSTRAINT ledger_entries_type_check;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_type_check
    CHECK (type IN ('admin_topup', 'admin_adjust', 'plan_purchase', 'plan_renewal', 'api_charge', 'refund'));
DROP INDEX request_logs_key;
DROP INDEX usage_hourly_key;
ALTER TABLE api_keys
    DROP COLUMN usage_reset_at, DROP COLUMN usage_version, DROP COLUMN used_balance_micro, DROP COLUMN used_all_micro,
    DROP COLUMN allow_owner_query, DROP COLUMN ip_allowlist, DROP COLUMN model_allowlist, DROP COLUMN expires_at,
    DROP COLUMN limit_7d_micro, DROP COLUMN limit_1d_micro, DROP COLUMN limit_5h_micro, DROP COLUMN quota_scope,
    DROP COLUMN quota_limit_micro;
