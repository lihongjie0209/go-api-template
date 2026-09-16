CREATE TABLE file_upload_intents (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    object_key text NOT NULL UNIQUE,
    status text NOT NULL CHECK (status IN ('pending', 'completed', 'abandoned')),
    attempts bigint NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    next_attempt_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted_at timestamptz,
    deleted_by text
);

CREATE INDEX file_upload_intents_pending_idx
    ON file_upload_intents (next_attempt_at, id)
    WHERE status = 'pending' AND deleted_at IS NULL;

SELECT app_enable_audit('file_upload_intents');
