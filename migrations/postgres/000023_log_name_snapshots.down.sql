ALTER TABLE security_logs
    DROP COLUMN IF EXISTS subject_name_snapshot,
    DROP COLUMN IF EXISTS actor_name_snapshot;

ALTER TABLE operation_logs
    DROP COLUMN IF EXISTS resource_name_snapshot,
    DROP COLUMN IF EXISTS actor_name_snapshot;
