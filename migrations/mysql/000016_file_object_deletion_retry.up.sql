ALTER TABLE files
    ADD COLUMN object_deleted_at timestamp(6) NULL,
    ADD COLUMN object_delete_attempts bigint NOT NULL DEFAULT 0,
    ADD COLUMN object_delete_error text NOT NULL,
    ADD COLUMN object_delete_next_at timestamp(6) NULL;

CREATE INDEX files_object_delete_pending_idx
    ON files (object_delete_next_at, id(128));
