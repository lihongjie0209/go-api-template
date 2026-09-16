CREATE TABLE event_outbox (
    id text PRIMARY KEY,
    subject text NOT NULL,
    envelope bytea NOT NULL,
    trace_parent text NOT NULL DEFAULT '',
    trace_state text NOT NULL DEFAULT '',
    available_at timestamptz NOT NULL,
    attempts bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    locked_by text NOT NULL DEFAULT '',
    locked_until timestamptz,
    published_at timestamptz,
    dead_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted_at timestamptz,
    deleted_by text
);
CREATE INDEX event_outbox_pending_idx ON event_outbox (available_at, created_at, id) WHERE published_at IS NULL AND dead_at IS NULL AND deleted_at IS NULL;
SELECT app_enable_audit('event_outbox');
