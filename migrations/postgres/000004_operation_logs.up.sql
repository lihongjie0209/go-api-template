CREATE TABLE operation_logs (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    actor_id text NOT NULL,
    actor_type text NOT NULL,
    application_id text NOT NULL DEFAULT '',
    source text NOT NULL,
    operation text NOT NULL,
    resource_type text NOT NULL DEFAULT '',
    resource_id text NOT NULL DEFAULT '',
    protocol text NOT NULL,
    method text NOT NULL DEFAULT '',
    route text NOT NULL DEFAULT '',
    request_payload text NOT NULL DEFAULT '',
    duration_ms bigint NOT NULL CHECK (duration_ms >= 0),
    succeeded boolean NOT NULL,
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    request_id text NOT NULL DEFAULT '',
    trace_id text NOT NULL DEFAULT '',
    client_ip text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    extension jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted_at timestamptz,
    deleted_by text
);
CREATE INDEX operation_logs_tenant_occurred_idx ON operation_logs (tenant_id, occurred_at DESC, id);
CREATE INDEX operation_logs_actor_occurred_idx ON operation_logs (actor_id, occurred_at DESC);
CREATE INDEX operation_logs_request_id_idx ON operation_logs (request_id) WHERE request_id <> '';
CREATE INDEX operation_logs_occurred_brin_idx ON operation_logs USING brin (occurred_at);
SELECT app_enable_audit('operation_logs');
