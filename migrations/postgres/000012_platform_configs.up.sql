CREATE TABLE platform_configs (
    id text PRIMARY KEY, config_key text NOT NULL, name text NOT NULL, category text NOT NULL DEFAULT '', value jsonb NOT NULL,
    description text NOT NULL DEFAULT '', is_public boolean NOT NULL DEFAULT false, status text NOT NULL CHECK(status IN('active','disabled')),
    created_at timestamptz NOT NULL, created_by text NOT NULL, updated_at timestamptz NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL CHECK(version>0), deleted_at timestamptz, deleted_by text
);
CREATE UNIQUE INDEX platform_configs_key_unique ON platform_configs(lower(config_key)) WHERE deleted_at IS NULL;
CREATE INDEX platform_configs_public_idx ON platform_configs(is_public,status,category) WHERE deleted_at IS NULL;
SELECT app_enable_audit('platform_configs');
