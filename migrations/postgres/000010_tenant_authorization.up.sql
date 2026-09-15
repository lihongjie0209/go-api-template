CREATE TABLE tenant_permission_grants (
 id text PRIMARY KEY, tenant_id text NOT NULL REFERENCES tenants(id), permission_id text NOT NULL REFERENCES permissions(id),
 created_at timestamptz NOT NULL,created_by text NOT NULL,updated_at timestamptz NOT NULL,updated_by text NOT NULL,version bigint NOT NULL CHECK(version>0),deleted_at timestamptz,deleted_by text);
CREATE UNIQUE INDEX tenant_permission_grants_unique ON tenant_permission_grants(tenant_id,permission_id) WHERE deleted_at IS NULL;
CREATE INDEX tenant_permission_grants_tenant_idx ON tenant_permission_grants(tenant_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('tenant_permission_grants');
CREATE TABLE tenant_administrators (
 id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants(id),membership_id text NOT NULL REFERENCES tenant_memberships(id),
 created_at timestamptz NOT NULL,created_by text NOT NULL,updated_at timestamptz NOT NULL,updated_by text NOT NULL,version bigint NOT NULL CHECK(version>0),deleted_at timestamptz,deleted_by text);
CREATE UNIQUE INDEX tenant_administrators_unique ON tenant_administrators(tenant_id,membership_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('tenant_administrators');
CREATE TABLE tenant_roles (
 id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants(id),code text NOT NULL,name text NOT NULL,description text NOT NULL DEFAULT '',status text NOT NULL CHECK(status IN('active','disabled')),
 created_at timestamptz NOT NULL,created_by text NOT NULL,updated_at timestamptz NOT NULL,updated_by text NOT NULL,version bigint NOT NULL CHECK(version>0),deleted_at timestamptz,deleted_by text);
CREATE UNIQUE INDEX tenant_roles_code_unique ON tenant_roles(tenant_id,lower(code)) WHERE deleted_at IS NULL;
SELECT app_enable_audit('tenant_roles');
CREATE TABLE tenant_role_permissions (
 id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants(id),role_id text NOT NULL REFERENCES tenant_roles(id),permission_id text NOT NULL REFERENCES permissions(id),
 created_at timestamptz NOT NULL,created_by text NOT NULL,updated_at timestamptz NOT NULL,updated_by text NOT NULL,version bigint NOT NULL CHECK(version>0),deleted_at timestamptz,deleted_by text);
CREATE UNIQUE INDEX tenant_role_permissions_unique ON tenant_role_permissions(tenant_id,role_id,permission_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('tenant_role_permissions');
CREATE TABLE tenant_member_roles (
 id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants(id),membership_id text NOT NULL REFERENCES tenant_memberships(id),role_id text NOT NULL REFERENCES tenant_roles(id),
 created_at timestamptz NOT NULL,created_by text NOT NULL,updated_at timestamptz NOT NULL,updated_by text NOT NULL,version bigint NOT NULL CHECK(version>0),deleted_at timestamptz,deleted_by text);
CREATE UNIQUE INDEX tenant_member_roles_unique ON tenant_member_roles(tenant_id,membership_id,role_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('tenant_member_roles');
