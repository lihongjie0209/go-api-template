CREATE TABLE files (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    object_key text NOT NULL UNIQUE,
    original_name text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    etag text NOT NULL DEFAULT '',
    checksum_sha256 text NOT NULL,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted_at timestamptz,
    deleted_by text
);
CREATE INDEX files_tenant_created_idx ON files (tenant_id, created_at DESC, id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('files');
