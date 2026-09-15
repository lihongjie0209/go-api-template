ALTER TABLE files
    ADD COLUMN object_deleted_at timestamptz,
    ADD COLUMN object_delete_attempts bigint NOT NULL DEFAULT 0,
    ADD COLUMN object_delete_error text NOT NULL DEFAULT '',
    ADD COLUMN object_delete_next_at timestamptz;

CREATE INDEX files_object_delete_pending_idx
    ON files (object_delete_next_at, id)
    WHERE deleted_at IS NOT NULL AND object_deleted_at IS NULL;
