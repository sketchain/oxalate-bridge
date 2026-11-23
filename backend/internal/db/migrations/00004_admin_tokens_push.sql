-- +goose Up
-- v0.5.0: admin API tokens, Web Push subscriptions and notification
-- preferences, user locale.

CREATE TABLE admin_tokens (
    id            uuid PRIMARY KEY,
    name          text NOT NULL,
    -- SHA-256 of the full token ("oba-" + 43 base62 characters); the token
    -- itself is never stored.
    token_hash    bytea NOT NULL,
    token_prefix  text NOT NULL,
    scope         text NOT NULL CHECK (scope IN ('read', 'write')),
    created_by    uuid NOT NULL REFERENCES users (id),
    expires_at    timestamptz,
    -- NULL = any address. Canonical prefixes (a single address is a /32 or /128).
    ip_allowlist  text[],
    last_used_at  timestamptz,
    last_used_ip  text NOT NULL DEFAULT '',
    revoked_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX admin_tokens_hash ON admin_tokens (token_hash);
CREATE INDEX admin_tokens_created ON admin_tokens (created_at DESC, id DESC);

-- Writes made with an admin token: operator_id is the token's creator.
ALTER TABLE audit_logs
    ADD COLUMN token_id   uuid,
    ADD COLUMN token_name text NOT NULL DEFAULT '';

ALTER TABLE users
    ADD COLUMN locale text NOT NULL DEFAULT 'zh-CN' CHECK (locale IN ('zh-CN', 'en'));

CREATE TABLE push_subscriptions (
    id               uuid PRIMARY KEY,
    user_id          uuid NOT NULL REFERENCES users (id),
    endpoint         text NOT NULL,
    p256dh           text NOT NULL,
    auth             text NOT NULL,
    device_label     text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_success_at  timestamptz,
    failures         integer NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX push_subscriptions_endpoint ON push_subscriptions (endpoint);
CREATE INDEX push_subscriptions_user ON push_subscriptions (user_id, created_at DESC);

-- One row per user and event type the user changed; missing rows fall back
-- to the per-role defaults. threshold_micro is used by balance_low.
CREATE TABLE notification_prefs (
    user_id          uuid NOT NULL REFERENCES users (id),
    event            text NOT NULL,
    enabled          boolean NOT NULL,
    threshold_micro  bigint CHECK (threshold_micro IS NULL OR threshold_micro >= 0),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, event)
);

-- +goose Down
DROP TABLE notification_prefs;
DROP TABLE push_subscriptions;
ALTER TABLE users DROP COLUMN locale;
ALTER TABLE audit_logs DROP COLUMN token_name, DROP COLUMN token_id;
DROP TABLE admin_tokens;
