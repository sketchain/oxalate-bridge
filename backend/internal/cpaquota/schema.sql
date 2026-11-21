-- Reference copy of the cpa-quota tables this bridge reads (cpa-quota
-- schema version 1, github.com/sketchain/cpa-quota internal/store/schema.go).
-- It exists only so sqlc can type-check internal/cpaquota/queries; it is NOT
-- a migration. cpa-quota creates and migrates these tables itself in
-- BRIDGE_CPA_QUOTA_SCHEMA; the bridge only ever reads them.

CREATE TABLE requests (
    id                 bigserial PRIMARY KEY,
    instance           text             NOT NULL,
    auth_index         text             NOT NULL,
    provider           text             NOT NULL,
    request_id         text             NOT NULL DEFAULT '',
    requested_ns       bigint           NOT NULL,
    latency_ms         bigint           NOT NULL DEFAULT 0,
    completed_ns       bigint           NOT NULL,
    requested_at       timestamptz GENERATED ALWAYS AS (CASE WHEN requested_ns = 0 THEN NULL ELSE to_timestamp(requested_ns::double precision / 1e9) END) STORED,
    model              text             NOT NULL DEFAULT '',
    upstream_model     text             NOT NULL DEFAULT '',
    service_tier       text             NOT NULL DEFAULT '',
    stream             boolean          NOT NULL DEFAULT false,
    input_tokens       bigint           NOT NULL DEFAULT 0,
    cache_read_tokens  bigint           NOT NULL DEFAULT 0,
    cache_write_tokens bigint           NOT NULL DEFAULT 0,
    output_tokens      bigint           NOT NULL DEFAULT 0,
    reasoning_tokens   bigint           NOT NULL DEFAULT 0,
    total_tokens       bigint           NOT NULL DEFAULT 0,
    accounting         text             NOT NULL DEFAULT '',
    cost_usd           double precision NOT NULL DEFAULT 0,
    priced             boolean          NOT NULL DEFAULT false,
    main_window        boolean          NOT NULL DEFAULT false,
    failed             boolean          NOT NULL DEFAULT false,
    status_code        integer          NOT NULL DEFAULT 0
);

CREATE TABLE quota_reports (
    id                bigserial PRIMARY KEY,
    instance          text    NOT NULL,
    auth_index        text    NOT NULL,
    provider          text    NOT NULL,
    request_id        text    NOT NULL DEFAULT '',
    at_ns             bigint  NOT NULL,
    at                timestamptz GENERATED ALWAYS AS (CASE WHEN at_ns = 0 THEN NULL ELSE to_timestamp(at_ns::double precision / 1e9) END) STORED,
    plan              text    NOT NULL DEFAULT '',
    credit_balance    double precision,
    credits_unlimited boolean,
    limit_reached     boolean NOT NULL DEFAULT false
);

CREATE TABLE quota_report_windows (
    report_id    bigint           NOT NULL REFERENCES quota_reports (id) ON DELETE CASCADE,
    instance     text             NOT NULL,
    auth_index   text             NOT NULL,
    provider     text             NOT NULL,
    limit_key    text             NOT NULL,
    seconds      bigint           NOT NULL,
    at_ns        bigint           NOT NULL,
    at           timestamptz GENERATED ALWAYS AS (CASE WHEN at_ns = 0 THEN NULL ELSE to_timestamp(at_ns::double precision / 1e9) END) STORED,
    used_percent double precision NOT NULL,
    reset_ns     bigint           NOT NULL DEFAULT 0,
    reset_at     timestamptz GENERATED ALWAYS AS (CASE WHEN reset_ns = 0 THEN NULL ELSE to_timestamp(reset_ns::double precision / 1e9) END) STORED,
    PRIMARY KEY (report_id, limit_key, seconds)
);

CREATE TABLE estimates (
    instance                text             NOT NULL,
    auth_index              text             NOT NULL,
    provider                text             NOT NULL,
    subscription            text             NOT NULL,
    tier                    text             NOT NULL,
    state                   text             NOT NULL,
    pause_reason            text             NOT NULL DEFAULT '',
    segment_window          text             NOT NULL,
    basis                   text             NOT NULL,
    k                       double precision,
    k_source                text             NOT NULL,
    totals_from             text             NOT NULL,
    safety_factor           double precision NOT NULL,
    used_5h                 double precision NOT NULL,
    reset_5h                timestamptz,
    used_7d                 double precision NOT NULL,
    reset_7d                timestamptz,
    five_hour_total_usd     double precision NOT NULL,
    weekly_total_usd        double precision NOT NULL,
    remaining_five_hour_usd double precision NOT NULL,
    remaining_week_usd      double precision NOT NULL,
    clean_blocks            integer          NOT NULL,
    total_blocks            integer          NOT NULL,
    segments                integer          NOT NULL,
    excluded_segments       integer          NOT NULL,
    ratio                   double precision NOT NULL,
    observed_at             timestamptz,
    estimated_at            timestamptz      NOT NULL,
    detail                  jsonb            NOT NULL,
    PRIMARY KEY (instance, auth_index)
);

CREATE TABLE k_trend_daily (
    instance          text             NOT NULL,
    auth_index        text             NOT NULL,
    day               date             NOT NULL,
    gain_5h           double precision NOT NULL,
    gain_7d           double precision NOT NULL,
    k                 double precision,
    segments          integer          NOT NULL,
    excluded_segments integer          NOT NULL,
    cost_usd          double precision NOT NULL,
    gain              double precision NOT NULL,
    updated_at        timestamptz      NOT NULL DEFAULT now(),
    PRIMARY KEY (instance, auth_index, day)
);
