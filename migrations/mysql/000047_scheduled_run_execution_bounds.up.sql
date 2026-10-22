-- MySQL 8.4 enforces CHECK on INSERT and UPDATE; OCTET_LENGTH counts payload bytes.
ALTER TABLE scheduled_job_runs ADD CONSTRAINT scheduled_job_runs_execution_bounds_check CHECK (timeout_seconds BETWEEN 1 AND 3600 AND lock_ttl_seconds BETWEEN 1 AND 86400 AND OCTET_LENGTH(payload) <= 1048576);
