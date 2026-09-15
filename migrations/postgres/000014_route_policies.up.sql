CREATE TABLE route_definitions (
    id text PRIMARY KEY,
    protocol text NOT NULL CHECK (protocol IN ('http', 'grpc')),
    method text NOT NULL,
    path text NOT NULL,
    operation text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    service_name text NOT NULL,
    source_version text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN ('active', 'inactive')),
    last_discovered_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL, created_by text NOT NULL,
    updated_at timestamptz NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0), deleted_at timestamptz, deleted_by text
);
CREATE UNIQUE INDEX route_definitions_identity_unique
    ON route_definitions (protocol, method, path, service_name) WHERE deleted_at IS NULL;
CREATE INDEX route_definitions_status_idx
    ON route_definitions (service_name, status, protocol) WHERE deleted_at IS NULL;
SELECT app_enable_audit('route_definitions');

CREATE TABLE route_policy_definitions (
    id text PRIMARY KEY,
    route_id text NOT NULL REFERENCES route_definitions(id),
    expression text NOT NULL,
    description text NOT NULL DEFAULT '',
    priority bigint NOT NULL DEFAULT 0,
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL, created_by text NOT NULL,
    updated_at timestamptz NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0), deleted_at timestamptz, deleted_by text
);
CREATE UNIQUE INDEX route_policy_definitions_route_unique
    ON route_policy_definitions (route_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('route_policy_definitions');

CREATE TABLE route_policy_permission_refs (
    id text PRIMARY KEY,
    policy_id text NOT NULL REFERENCES route_policy_definitions(id),
    permission_id text NOT NULL REFERENCES permissions(id),
    scope text NOT NULL CHECK (scope IN ('tenant', 'platform', 'principal')),
    created_at timestamptz NOT NULL, created_by text NOT NULL,
    updated_at timestamptz NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0), deleted_at timestamptz, deleted_by text
);
CREATE UNIQUE INDEX route_policy_permission_refs_unique
    ON route_policy_permission_refs (policy_id, permission_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('route_policy_permission_refs');
