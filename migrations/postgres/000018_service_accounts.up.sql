CREATE TABLE identity_service_accounts (
    id text PRIMARY KEY,
    client_id text NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    secret_hash text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    expires_at timestamptz NULL,
    last_used_at timestamptz NULL,
	failed_attempts bigint NOT NULL DEFAULT 0,
	locked_until timestamptz NULL,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted_at timestamptz NULL,
    deleted_by text NULL
);

CREATE UNIQUE INDEX identity_service_accounts_client_unique
	ON identity_service_accounts (lower(client_id));
CREATE INDEX identity_service_accounts_status_created_idx
    ON identity_service_accounts (status, created_at DESC) WHERE deleted_at IS NULL;
SELECT app_enable_audit('identity_service_accounts');
