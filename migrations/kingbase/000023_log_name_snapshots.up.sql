ALTER TABLE operation_logs ADD COLUMN actor_name_snapshot text NOT NULL DEFAULT '';
ALTER TABLE operation_logs ADD COLUMN resource_name_snapshot text NOT NULL DEFAULT '';
ALTER TABLE security_logs ADD COLUMN actor_name_snapshot text NOT NULL DEFAULT '';
ALTER TABLE security_logs ADD COLUMN subject_name_snapshot text NOT NULL DEFAULT '';
