CREATE TABLE scheduled_job_runs (
    id text PRIMARY KEY,
    scheduled_job_id text NOT NULL REFERENCES scheduled_jobs(id),
    job_code text NOT NULL,
    job_name text NOT NULL,
    handler text NOT NULL,
    trigger_source text NOT NULL CHECK(trigger_source IN('cron','manual')),
    status text NOT NULL CHECK(status IN('success','error','skipped')),
    request_id text NOT NULL,
    trace_id text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    duration_ms bigint NOT NULL CHECK(duration_ms>=0),
    error_message text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK(version>0),
    deleted_at timestamptz,
    deleted_by text
);
CREATE INDEX scheduled_job_runs_page_idx ON scheduled_job_runs(scheduled_job_id,started_at DESC,id) WHERE deleted_at IS NULL;
CREATE INDEX scheduled_job_runs_status_idx ON scheduled_job_runs(status,started_at DESC,id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('scheduled_job_runs');
