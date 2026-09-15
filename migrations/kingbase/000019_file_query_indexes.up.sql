CREATE INDEX files_tenant_content_created_idx
    ON files (tenant_id, content_type, created_at DESC, id)
    WHERE deleted_at IS NULL;
CREATE INDEX files_tenant_creator_created_idx
    ON files (tenant_id, created_by, created_at DESC, id)
    WHERE deleted_at IS NULL;
CREATE INDEX files_tenant_size_created_idx
    ON files (tenant_id, size_bytes, created_at DESC, id)
    WHERE deleted_at IS NULL;
