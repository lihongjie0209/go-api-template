DROP INDEX files_object_delete_pending_idx ON files;
CREATE INDEX files_object_delete_pending_idx
    ON files (object_deleted_at, object_delete_next_at, deleted_at, id(64));
CREATE INDEX files_tenant_content_created_idx
    ON files (tenant_id(64), content_type(64), created_at, id(64));
CREATE INDEX files_tenant_creator_created_idx
    ON files (tenant_id(64), created_by(64), created_at, id(64));
CREATE INDEX files_tenant_size_created_idx
    ON files (tenant_id(64), size_bytes, created_at, id(64));
