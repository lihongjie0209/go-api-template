CREATE TABLE permissions (
    id text PRIMARY KEY,
    parent_id text REFERENCES permissions(id), permission_key text NOT NULL, name text NOT NULL,
    node_type text NOT NULL CHECK (node_type IN ('group', 'permission')),
    resource text NOT NULL DEFAULT '', action text NOT NULL DEFAULT '', description text NOT NULL DEFAULT '',
    sort_order bigint NOT NULL DEFAULT 0, status text NOT NULL CHECK (status IN ('active', 'disabled')),
    is_system boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL, created_by text NOT NULL, updated_at timestamptz NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0), deleted_at timestamptz, deleted_by text
);
CREATE UNIQUE INDEX permissions_key_unique ON permissions (lower(permission_key)) WHERE deleted_at IS NULL;
CREATE INDEX permissions_tree_idx ON permissions (parent_id, sort_order, id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('permissions');
