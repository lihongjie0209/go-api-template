ALTER TABLE security_logs
    DROP COLUMN subject_name_snapshot,
    DROP COLUMN actor_name_snapshot;

ALTER TABLE operation_logs
    DROP COLUMN resource_name_snapshot,
    DROP COLUMN actor_name_snapshot;
