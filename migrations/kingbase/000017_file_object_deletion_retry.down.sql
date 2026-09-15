DROP INDEX IF EXISTS files_object_delete_pending_idx;
ALTER TABLE files DROP COLUMN object_delete_next_at;
ALTER TABLE files DROP COLUMN object_delete_error;
ALTER TABLE files DROP COLUMN object_delete_attempts;
ALTER TABLE files DROP COLUMN object_deleted_at;
