CREATE TABLE identity_refresh_token_history (
    token_hash text PRIMARY KEY,
    session_id text NOT NULL REFERENCES identity_sessions(id),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK(version > 0),
    deleted_at timestamptz,
    deleted_by text,
    CONSTRAINT identity_refresh_token_history_hash_length CHECK(length(token_hash) = 64)
);
CREATE INDEX identity_refresh_token_history_session_idx ON identity_refresh_token_history(session_id) WHERE deleted_at IS NULL;
CREATE INDEX identity_refresh_token_history_expiry_idx ON identity_refresh_token_history(expires_at,token_hash) WHERE deleted_at IS NULL;
SELECT app_enable_audit('identity_refresh_token_history');
