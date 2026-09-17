CREATE TABLE tenant_application_grants (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES tenants(id),
    application_id text NOT NULL REFERENCES applications(id),
    status text NOT NULL CHECK(status IN ('active','revoked')),
    starts_at timestamptz,
    expires_at timestamptz,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK(version > 0),
    deleted_at timestamptz,
    deleted_by text,
    UNIQUE(tenant_id, application_id),
    CHECK(expires_at IS NULL OR starts_at IS NULL OR expires_at > starts_at)
);
CREATE INDEX tenant_application_grants_current_idx ON tenant_application_grants(tenant_id,status,application_id) WHERE deleted_at IS NULL;
CREATE INDEX tenant_application_grants_application_idx ON tenant_application_grants(application_id,tenant_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('tenant_application_grants');
