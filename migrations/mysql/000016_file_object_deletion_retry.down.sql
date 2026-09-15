DROP INDEX files_object_delete_pending_idx ON files;
ALTER TABLE files
    DROP COLUMN object_delete_next_at,
    DROP COLUMN object_delete_error,
    DROP COLUMN object_delete_attempts,
    DROP COLUMN object_deleted_at;
