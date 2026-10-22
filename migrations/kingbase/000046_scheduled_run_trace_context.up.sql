-- A durable manual run must retain its submitter's W3C trace parent across worker restarts.
ALTER TABLE scheduled_job_runs ADD COLUMN trace_parent text NOT NULL DEFAULT '';
ALTER TABLE scheduled_job_runs ADD COLUMN trace_state text NOT NULL DEFAULT '';
ALTER TABLE scheduled_job_runs ADD CONSTRAINT scheduled_job_runs_trace_context_length_check CHECK (length(trace_parent) <= 55 AND length(trace_state) <= 512);
