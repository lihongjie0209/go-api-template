DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM scheduled_job_runs WHERE status IN ('queued','running')) THEN
        RAISE EXCEPTION 'cannot downgrade while manual scheduled jobs are pending';
    END IF;
END $$;
DROP INDEX scheduled_job_runs_queued_idx;
DROP INDEX scheduled_job_runs_running_idx;
DROP INDEX scheduled_job_runs_created_idx;
ALTER TABLE scheduled_job_runs DROP COLUMN dispatched_at;
ALTER TABLE scheduled_job_runs DROP COLUMN lock_ttl_seconds;
ALTER TABLE scheduled_job_runs DROP COLUMN timeout_seconds;
ALTER TABLE scheduled_job_runs DROP COLUMN payload;
ALTER TABLE scheduled_job_runs DROP COLUMN lease_until;
ALTER TABLE scheduled_job_runs DROP COLUMN lease_owner;
ALTER TABLE scheduled_job_runs DROP CONSTRAINT scheduled_job_runs_status_check;
ALTER TABLE scheduled_job_runs ADD CONSTRAINT scheduled_job_runs_status_check CHECK(status IN('success','error','skipped'));
ALTER TABLE scheduled_job_runs ALTER COLUMN started_at SET NOT NULL;
ALTER TABLE scheduled_job_runs ALTER COLUMN finished_at SET NOT NULL;
