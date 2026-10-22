ALTER TABLE scheduled_job_runs DROP CONSTRAINT scheduled_job_runs_trace_context_length_check;
ALTER TABLE scheduled_job_runs DROP COLUMN trace_state;
ALTER TABLE scheduled_job_runs DROP COLUMN trace_parent;
