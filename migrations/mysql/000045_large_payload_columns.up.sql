-- Match the 1 MiB scheduled-run payload and 64 KiB operation-log summary contracts.
ALTER TABLE scheduled_job_runs MODIFY COLUMN payload MEDIUMTEXT NOT NULL DEFAULT ('{}');
ALTER TABLE operation_logs MODIFY COLUMN request_payload MEDIUMTEXT NOT NULL;
