CREATE TABLE security_logs (
    id text PRIMARY KEY,
    tenant_id text NOT NULL DEFAULT '',
    actor_id text NOT NULL DEFAULT '',
    actor_type text NOT NULL DEFAULT '',
    subject_id text NOT NULL DEFAULT '',
    subject_type text NOT NULL DEFAULT '',
    event_type text NOT NULL,
    succeeded boolean NOT NULL,
    reason text NOT NULL DEFAULT '',
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    identifier_hash text NOT NULL DEFAULT '',
    token_id_hash text NOT NULL DEFAULT '',
    session_id text NOT NULL DEFAULT '',
    request_id text NOT NULL DEFAULT '',
    trace_id text NOT NULL DEFAULT '',
    client_ip text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted_at timestamptz,
    deleted_by text
);
CREATE INDEX security_logs_subject_occurred_idx ON security_logs (subject_id, occurred_at DESC);
CREATE INDEX security_logs_identifier_occurred_idx ON security_logs (identifier_hash, occurred_at DESC);
CREATE INDEX security_logs_event_occurred_idx ON security_logs (event_type, occurred_at DESC);
CREATE INDEX security_logs_tenant_occurred_idx ON security_logs (tenant_id, occurred_at DESC, id DESC);
CREATE INDEX security_logs_tenant_event_occurred_idx ON security_logs (tenant_id, event_type, occurred_at DESC);
CREATE INDEX security_logs_tenant_subject_occurred_idx ON security_logs (tenant_id, subject_id, occurred_at DESC);
CREATE INDEX security_logs_request_idx ON security_logs (request_id);
SELECT app_enable_audit('security_logs');
