-- W3C trace context is bounded; ordinary runs retain empty defaults.
ALTER TABLE scheduled_job_runs ADD COLUMN trace_parent varchar(55) NOT NULL DEFAULT '', ADD COLUMN trace_state varchar(512) NOT NULL DEFAULT '';
