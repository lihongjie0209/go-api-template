CREATE TABLE operation_logs (
    id text NOT NULL,
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
    deleted_by text,
    PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);
CREATE INDEX operation_logs_tenant_occurred_idx ON operation_logs (tenant_id, occurred_at DESC, id);
CREATE INDEX operation_logs_actor_occurred_idx ON operation_logs (actor_id, occurred_at DESC);
CREATE INDEX operation_logs_tenant_operation_idx ON operation_logs (tenant_id, operation, occurred_at DESC);
CREATE INDEX operation_logs_tenant_resource_idx ON operation_logs (tenant_id, resource_type, resource_id, occurred_at DESC);
CREATE INDEX operation_logs_tenant_application_idx ON operation_logs (tenant_id, application_id, occurred_at DESC) WHERE application_id <> '';
CREATE INDEX operation_logs_request_id_idx ON operation_logs (request_id);
SELECT app_enable_audit('operation_logs');

DO $partition$
DECLARE
    offset_month integer;
    range_start timestamptz;
    range_end timestamptz;
    partition_name text;
BEGIN
    FOR offset_month IN -12..6 LOOP
        range_start := (date_trunc('month', CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai') + offset_month * interval '1 month') AT TIME ZONE 'Asia/Shanghai';
        range_end := range_start + interval '1 month';
        partition_name := 'operation_logs_y' || to_char(range_start AT TIME ZONE 'Asia/Shanghai', 'YYYY') || 'm' || to_char(range_start AT TIME ZONE 'Asia/Shanghai', 'MM');
        EXECUTE format('CREATE TABLE %I PARTITION OF operation_logs FOR VALUES FROM (%L) TO (%L)', partition_name, range_start, range_end);
    END LOOP;
END
$partition$;
CREATE TABLE operation_logs_default PARTITION OF operation_logs DEFAULT;
