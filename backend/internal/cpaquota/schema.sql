-- Reference copy of cpa-quota's tables, schema version 1, as created by
-- cpa-quota v0.1.0 (commit 3f2b333, internal/store/schema.go). The bridge
-- never migrates the real cpa-quota schema: sqlc type-checks the read-only
-- queries in queries/ against this file, and tests and `bridge seed-demo`
-- create a demonstration schema from it. TestUpstreamSchemaDrift compares it
-- with a schema migrated by the pinned plugin (see UPSTREAM and the Check
-- workflow); regenerate it from the plugin, do not edit it by hand.

CREATE TABLE meta (key text PRIMARY KEY, value text NOT NULL);
INSERT INTO meta (key, value) VALUES ('schema_version', '1');

CREATE TABLE requests (
	id                 bigserial PRIMARY KEY,
	instance           text             NOT NULL,
	auth_index         text             NOT NULL,
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
	failed             boolean          NOT NULL DEFAULT false,
	status_code        integer          NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX requests_request_id ON requests (instance, request_id) WHERE request_id <> '';
CREATE INDEX requests_account_time ON requests (instance, auth_index, requested_ns);

CREATE TABLE quota_reports (
	id         bigserial PRIMARY KEY,
	instance   text   NOT NULL,
	auth_index text   NOT NULL,
	request_id text   NOT NULL DEFAULT '',
	at_ns      bigint NOT NULL,
	at         timestamptz GENERATED ALWAYS AS (CASE WHEN at_ns = 0 THEN NULL ELSE to_timestamp(at_ns::double precision / 1e9) END) STORED
);
CREATE UNIQUE INDEX quota_reports_request_id ON quota_reports (instance, request_id) WHERE request_id <> '';
CREATE INDEX quota_reports_account_time ON quota_reports (instance, auth_index, at_ns);

CREATE TABLE quota_report_windows (
	report_id    bigint           NOT NULL REFERENCES quota_reports (id) ON DELETE CASCADE,
	instance     text             NOT NULL,
	auth_index   text             NOT NULL,
	limit_key    text             NOT NULL,
	seconds      bigint           NOT NULL,
	at_ns        bigint           NOT NULL,
	at           timestamptz GENERATED ALWAYS AS (CASE WHEN at_ns = 0 THEN NULL ELSE to_timestamp(at_ns::double precision / 1e9) END) STORED,
	used_percent double precision NOT NULL,
	reset_ns     bigint           NOT NULL DEFAULT 0,
	reset_at     timestamptz GENERATED ALWAYS AS (CASE WHEN reset_ns = 0 THEN NULL ELSE to_timestamp(reset_ns::double precision / 1e9) END) STORED,
	PRIMARY KEY (report_id, limit_key, seconds)
);
CREATE INDEX quota_report_windows_lookup ON quota_report_windows (instance, auth_index, limit_key, seconds, at_ns);

CREATE TABLE streams (
	instance   text        NOT NULL,
	auth_index text        NOT NULL,
	state      jsonb       NOT NULL,
	updated_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (instance, auth_index)
);

CREATE TABLE segments (
	id         bigserial PRIMARY KEY,
	instance   text             NOT NULL,
	auth_index text             NOT NULL,
	block_ns   bigint           NOT NULL,
	block_at   timestamptz GENERATED ALWAYS AS (CASE WHEN block_ns = 0 THEN NULL ELSE to_timestamp(block_ns::double precision / 1e9) END) STORED,
	at_ns      bigint           NOT NULL,
	at         timestamptz GENERATED ALWAYS AS (CASE WHEN at_ns = 0 THEN NULL ELSE to_timestamp(at_ns::double precision / 1e9) END) STORED,
	cost_usd   double precision NOT NULL,
	gain       double precision NOT NULL,
	models     jsonb
);
CREATE INDEX segments_account_time ON segments (instance, auth_index, at_ns);

CREATE TABLE k_buckets (
	instance   text             NOT NULL,
	auth_index text             NOT NULL,
	bucket_ns  bigint           NOT NULL,
	bucket_at  timestamptz GENERATED ALWAYS AS (CASE WHEN bucket_ns = 0 THEN NULL ELSE to_timestamp(bucket_ns::double precision / 1e9) END) STORED,
	gain_5h    double precision NOT NULL,
	gain_7d    double precision NOT NULL,
	PRIMARY KEY (instance, auth_index, bucket_ns)
);

CREATE TABLE blocks (
	instance       text             NOT NULL,
	auth_index     text             NOT NULL,
	start_at       timestamptz      NOT NULL,
	end_at         timestamptz      NOT NULL,
	segments       integer          NOT NULL,
	excluded       integer          NOT NULL,
	gain           double precision NOT NULL,
	cost_usd       double precision NOT NULL,
	clean_gain     double precision NOT NULL,
	clean_cost_usd double precision NOT NULL,
	reference      double precision NOT NULL,
	reference_mode text             NOT NULL,
	clean          boolean          NOT NULL,
	PRIMARY KEY (instance, auth_index, start_at)
);

CREATE TABLE estimates (
	instance                text             NOT NULL,
	auth_index              text             NOT NULL,
	tier                    text             NOT NULL,
	state                   text             NOT NULL,
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

CREATE TABLE model_weights (
	instance   text        PRIMARY KEY,
	enabled    boolean     NOT NULL,
	basis      text        NOT NULL,
	effective  jsonb       NOT NULL,
	estimate   jsonb       NOT NULL,
	models     jsonb       NOT NULL,
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE calibrations (
	id         bigserial PRIMARY KEY,
	instance   text   NOT NULL,
	auth_index text   NOT NULL,
	item       jsonb  NOT NULL,
	started_ns bigint NOT NULL,
	started_at timestamptz GENERATED ALWAYS AS (CASE WHEN started_ns = 0 THEN NULL ELSE to_timestamp(started_ns::double precision / 1e9) END) STORED,
	ended_ns   bigint NOT NULL DEFAULT 0,
	ended_at   timestamptz GENERATED ALWAYS AS (CASE WHEN ended_ns = 0 THEN NULL ELSE to_timestamp(ended_ns::double precision / 1e9) END) STORED,
	result     jsonb
);
CREATE INDEX calibrations_instance ON calibrations (instance, started_ns);

CREATE TABLE price_tables (
	instance   text        PRIMARY KEY,
	source_url text        NOT NULL,
	fetched_at timestamptz NOT NULL,
	models     integer     NOT NULL,
	body       jsonb       NOT NULL
);

CREATE TABLE safeguard_events (
	id          bigserial PRIMARY KEY,
	instance    text    NOT NULL,
	at_ns       bigint  NOT NULL,
	at          timestamptz GENERATED ALWAYS AS (CASE WHEN at_ns = 0 THEN NULL ELSE to_timestamp(at_ns::double precision / 1e9) END) STORED,
	auth_index  text    NOT NULL DEFAULT '',
	model       text    NOT NULL DEFAULT '',
	category    text    NOT NULL DEFAULT '',
	reason      text    NOT NULL DEFAULT '',
	request_id  text    NOT NULL DEFAULT '',
	status_code integer NOT NULL DEFAULT 0,
	stream      boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX safeguard_events_request_id ON safeguard_events (instance, request_id) WHERE request_id <> '';
CREATE INDEX safeguard_events_time ON safeguard_events (instance, at_ns);
