CREATE TABLE tenants (
    id text PRIMARY KEY,
    code text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    owner_user_id text NOT NULL,
    owner_name text NOT NULL,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted_at timestamptz,
    deleted_by text
);
CREATE UNIQUE INDEX tenants_code_unique ON tenants (lower(code)) WHERE deleted_at IS NULL;
CREATE INDEX tenants_name_idx ON tenants (lower(name)) WHERE deleted_at IS NULL;
SELECT app_enable_audit('tenants');

CREATE TABLE tenant_departments (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES tenants(id),
    parent_id text REFERENCES tenant_departments(id),
    code text NOT NULL,
    name text NOT NULL,
    sort_order bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted_at timestamptz,
    deleted_by text
);
CREATE UNIQUE INDEX tenant_departments_code_unique ON tenant_departments (tenant_id, lower(code)) WHERE deleted_at IS NULL;
CREATE INDEX tenant_departments_parent_idx ON tenant_departments (tenant_id, parent_id, sort_order) WHERE deleted_at IS NULL;
SELECT app_enable_audit('tenant_departments');

CREATE TABLE tenant_memberships (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES tenants(id),
    user_id text NOT NULL,
    username text NOT NULL,
    display_name text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    joined_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted_at timestamptz,
    deleted_by text
);
CREATE UNIQUE INDEX tenant_memberships_user_unique ON tenant_memberships (tenant_id, user_id) WHERE deleted_at IS NULL;
CREATE INDEX tenant_memberships_user_idx ON tenant_memberships (user_id, status) WHERE deleted_at IS NULL;
SELECT app_enable_audit('tenant_memberships');
