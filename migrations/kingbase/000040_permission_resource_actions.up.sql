CREATE TABLE permission_resource_actions (
    id text PRIMARY KEY,
    permission_id text NOT NULL REFERENCES permissions(id),
    action_id text NOT NULL REFERENCES authorization_resource_actions(id),
    created_at timestamptz NOT NULL, created_by text NOT NULL,
    updated_at timestamptz NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0), deleted_at timestamptz, deleted_by text
);
CREATE UNIQUE INDEX permission_resource_actions_unique ON permission_resource_actions(permission_id,action_id) WHERE deleted_at IS NULL;
CREATE INDEX permission_resource_actions_action_idx ON permission_resource_actions(action_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('permission_resource_actions');

CREATE TABLE platform_principal_permissions (
    id text PRIMARY KEY,
    principal_type text NOT NULL CHECK (principal_type IN ('user','service_account')),
    principal_id text NOT NULL,
    permission_id text NOT NULL REFERENCES permissions(id),
    created_at timestamptz NOT NULL, created_by text NOT NULL,
    updated_at timestamptz NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0), deleted_at timestamptz, deleted_by text
);
CREATE UNIQUE INDEX platform_principal_permissions_unique ON platform_principal_permissions(principal_type,principal_id,permission_id) WHERE deleted_at IS NULL;
CREATE INDEX platform_principal_permissions_principal_idx ON platform_principal_permissions(principal_type,principal_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('platform_principal_permissions');

DROP TABLE pbac_policy_actions;
DROP TABLE pbac_policy_versions;
DROP TABLE pbac_policies;
