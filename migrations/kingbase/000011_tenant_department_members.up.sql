CREATE TABLE tenant_department_members (id text PRIMARY KEY,tenant_id text NOT NULL REFERENCES tenants(id),department_id text NOT NULL REFERENCES tenant_departments(id),membership_id text NOT NULL REFERENCES tenant_memberships(id),is_primary boolean NOT NULL DEFAULT false,created_at timestamptz NOT NULL,created_by text NOT NULL,updated_at timestamptz NOT NULL,updated_by text NOT NULL,version bigint NOT NULL CHECK(version>0),deleted_at timestamptz,deleted_by text);
CREATE UNIQUE INDEX tenant_department_members_unique ON tenant_department_members(tenant_id,department_id,membership_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX tenant_department_members_primary_unique ON tenant_department_members(tenant_id,membership_id) WHERE deleted_at IS NULL AND is_primary;
CREATE INDEX tenant_department_members_department_idx ON tenant_department_members(tenant_id,department_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('tenant_department_members');
