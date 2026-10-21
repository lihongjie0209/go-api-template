-- Strict mode prevents silent loss when narrowing populated payload columns.
SET @payload_previous_sql_mode=@@SESSION.sql_mode;
SET SESSION sql_mode='STRICT_ALL_TABLES';
ALTER TABLE operation_logs MODIFY COLUMN request_payload TEXT NOT NULL;
ALTER TABLE scheduled_job_runs MODIFY COLUMN payload TEXT NOT NULL DEFAULT ('{}');
SET SESSION sql_mode=@payload_previous_sql_mode;
