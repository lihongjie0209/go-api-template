DROP INDEX IF EXISTS files_object_delete_pending_idx;
ALTER TABLE files
    DROP COLUMN IF EXISTS object_delete_next_at,
    DROP COLUMN IF EXISTS object_delete_error,
    DROP COLUMN IF EXISTS object_delete_attempts,
    DROP COLUMN IF EXISTS object_deleted_at;
