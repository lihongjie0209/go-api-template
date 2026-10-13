CREATE TABLE authorization_resources (
    id text PRIMARY KEY,
    resource_key text NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    scope text NOT NULL CHECK (scope IN ('platform','tenant','principal')),
    source text NOT NULL CHECK (source IN ('code','manual')),
    status text NOT NULL CHECK (status IN ('active','disabled')),
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted_at timestamptz,
    deleted_by text
);
CREATE INDEX authorization_resources_source_idx ON authorization_resources (source,status,resource_key) WHERE deleted_at IS NULL;
SELECT app_enable_audit('authorization_resources');

CREATE TABLE authorization_resource_actions (
    id text PRIMARY KEY,
    resource_id text NOT NULL REFERENCES authorization_resources(id),
    action_key text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    source text NOT NULL CHECK (source IN ('code','manual')),
    status text NOT NULL CHECK (status IN ('active','disabled')),
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted_at timestamptz,
    deleted_by text,
    UNIQUE (resource_id,action_key)
);
CREATE INDEX authorization_resource_actions_source_idx ON authorization_resource_actions (source,status,resource_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('authorization_resource_actions');

CREATE TABLE authorization_catalog_syncs (
    source text PRIMARY KEY,
    build_time text NOT NULL DEFAULT '',
    commit_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted_at timestamptz,
    deleted_by text
);
SELECT app_enable_audit('authorization_catalog_syncs');
SELECT set_config('app.actor_id', 'system:migration', false);
INSERT INTO authorization_catalog_syncs (source,build_time,commit_id,created_at,created_by,updated_at,updated_by,version)
VALUES ('code','','',CURRENT_TIMESTAMP,'system:migration',CURRENT_TIMESTAMP,'system:migration',1);
