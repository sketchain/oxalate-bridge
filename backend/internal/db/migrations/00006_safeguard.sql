-- +goose Up
-- v0.6.0: safeguard protection. Every user has an allowance of safeguard
-- triggers shared by all their keys; once it is used up every key is refused
-- locally until a reset card or an approved appeal clears the count. The
-- request body of every trigger is kept (gzip) for review, and the hash of
-- its last user message goes on a site-wide blocklist.

ALTER TABLE users
    -- NULL: the safeguard_default_allowance setting.
    ADD COLUMN safeguard_allowance integer CHECK (safeguard_allowance IS NULL OR safeguard_allowance >= 1),
    ADD COLUMN safeguard_used integer NOT NULL DEFAULT 0 CHECK (safeguard_used >= 0),
    ADD COLUMN safeguard_reset_cards integer NOT NULL DEFAULT 0 CHECK (safeguard_reset_cards >= 0),
    -- When the count last reached the allowance (cleared by a reset).
    ADD COLUMN safeguard_locked_at timestamptz;

ALTER TABLE safeguard_events
    ADD COLUMN status_code   integer NOT NULL DEFAULT 0,
    ADD COLUMN stream        boolean NOT NULL DEFAULT false,
    -- Whether the trigger used up one of the user's allowance (a concurrent
    -- trigger of a prompt that was already blocked does not).
    ADD COLUMN counted       boolean NOT NULL DEFAULT true,
    -- sha256 of the normalized last user message ('' when too short).
    ADD COLUMN prompt_hash   text NOT NULL DEFAULT '',
    -- The normalized last user message (at most 4000 characters).
    ADD COLUMN prompt        text NOT NULL DEFAULT '',
    -- The request body as the client sent it, gzip-compressed.
    ADD COLUMN body_gz       bytea,
    ADD COLUMN body_size     bigint NOT NULL DEFAULT 0;
CREATE INDEX safeguard_events_user ON safeguard_events (user_id, created_at DESC, id DESC);
CREATE INDEX safeguard_events_hash ON safeguard_events (prompt_hash) WHERE prompt_hash <> '';

CREATE TABLE safeguard_blocks (
    hash           text PRIMARY KEY,
    prompt         text NOT NULL,
    first_event_id uuid NOT NULL,
    first_user_id  uuid,
    created_at     timestamptz NOT NULL DEFAULT now(),
    hits           bigint NOT NULL DEFAULT 0,
    last_hit_at    timestamptz,
    revoked_at     timestamptz,
    revoked_by     uuid
);
CREATE INDEX safeguard_blocks_created ON safeguard_blocks (created_at DESC, hash);

CREATE TABLE safeguard_blocked_requests (
    id          uuid PRIMARY KEY,
    request_id  uuid NOT NULL,
    user_id     uuid NOT NULL,
    key_id      uuid,
    hash        text NOT NULL,
    created_at  timestamptz NOT NULL
);
CREATE INDEX safeguard_blocked_requests_user ON safeguard_blocked_requests (user_id, created_at DESC, id DESC);

CREATE TABLE safeguard_appeals (
    id             uuid PRIMARY KEY,
    user_id        uuid NOT NULL REFERENCES users (id),
    note           text NOT NULL,
    status         text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    reject_reason  text NOT NULL DEFAULT '',
    decided_by     uuid,
    decided_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX safeguard_appeals_one_pending ON safeguard_appeals (user_id) WHERE status = 'pending';
CREATE INDEX safeguard_appeals_created ON safeguard_appeals (created_at DESC, id DESC);
CREATE INDEX safeguard_appeals_user ON safeguard_appeals (user_id, created_at DESC);

-- +goose Down
DROP TABLE safeguard_appeals;
DROP TABLE safeguard_blocked_requests;
DROP TABLE safeguard_blocks;
DROP INDEX safeguard_events_hash;
DROP INDEX safeguard_events_user;
ALTER TABLE safeguard_events DROP COLUMN body_size, DROP COLUMN body_gz, DROP COLUMN prompt, DROP COLUMN prompt_hash,
    DROP COLUMN counted, DROP COLUMN stream, DROP COLUMN status_code;
ALTER TABLE users DROP COLUMN safeguard_locked_at, DROP COLUMN safeguard_reset_cards, DROP COLUMN safeguard_used,
    DROP COLUMN safeguard_allowance;
