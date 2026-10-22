-- Durable execution snapshots must keep the same numeric and byte limits as definitions.
ALTER TABLE scheduled_job_runs ADD CONSTRAINT scheduled_job_runs_execution_bounds_check CHECK (timeout_seconds BETWEEN 1 AND 3600 AND lock_ttl_seconds BETWEEN 1 AND 86400 AND octet_length(payload) <= 1048576);
