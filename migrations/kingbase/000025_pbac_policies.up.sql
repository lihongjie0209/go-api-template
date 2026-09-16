CREATE TABLE pbac_policies (
    id text PRIMARY KEY, code text NOT NULL, name text NOT NULL, description text NOT NULL DEFAULT '',
    scope text NOT NULL CHECK(scope IN('global','tenant')), tenant_id text,
    published_version_number bigint CHECK(published_version_number > 0),
    status text NOT NULL CHECK(status IN('active','disabled')),
    created_at timestamptz NOT NULL, created_by text NOT NULL, updated_at timestamptz NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL CHECK(version > 0), deleted_at timestamptz, deleted_by text,
    CHECK((scope='global' AND tenant_id IS NULL) OR (scope='tenant' AND tenant_id IS NOT NULL AND tenant_id<>''))
);
CREATE UNIQUE INDEX pbac_policies_identity_unique ON pbac_policies(scope,COALESCE(tenant_id,''),lower(code)) WHERE deleted_at IS NULL;
CREATE INDEX pbac_policies_published_idx ON pbac_policies(status,scope,tenant_id) WHERE deleted_at IS NULL AND published_version_number IS NOT NULL;
SELECT app_enable_audit('pbac_policies');

CREATE TABLE pbac_policy_versions (
    id text PRIMARY KEY, policy_id text NOT NULL REFERENCES pbac_policies(id),
    version_number bigint NOT NULL CHECK(version_number > 0), document text NOT NULL,
    status text NOT NULL CHECK(status IN('draft','published','archived')),
    published_at timestamptz, published_by text,
    created_at timestamptz NOT NULL, created_by text NOT NULL, updated_at timestamptz NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL CHECK(version > 0), deleted_at timestamptz, deleted_by text,
    CHECK((status='draft' AND published_at IS NULL AND published_by IS NULL) OR (status IN('published','archived') AND published_at IS NOT NULL AND published_by IS NOT NULL))
);
CREATE UNIQUE INDEX pbac_policy_versions_number_unique ON pbac_policy_versions(policy_id,version_number) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX pbac_policy_versions_published_unique ON pbac_policy_versions(policy_id) WHERE deleted_at IS NULL AND status='published';
CREATE INDEX pbac_policy_versions_status_idx ON pbac_policy_versions(policy_id,status,version_number) WHERE deleted_at IS NULL;
SELECT app_enable_audit('pbac_policy_versions');

CREATE TABLE pbac_policy_actions (
    id text PRIMARY KEY, policy_version_id text NOT NULL REFERENCES pbac_policy_versions(id),
    resource text NOT NULL, action text NOT NULL,
    created_at timestamptz NOT NULL, created_by text NOT NULL, updated_at timestamptz NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL CHECK(version > 0), deleted_at timestamptz, deleted_by text
);
CREATE UNIQUE INDEX pbac_policy_actions_unique ON pbac_policy_actions(policy_version_id,resource,action) WHERE deleted_at IS NULL;
CREATE INDEX pbac_policy_actions_lookup_idx ON pbac_policy_actions(resource,action,policy_version_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('pbac_policy_actions');
