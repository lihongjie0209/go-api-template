CREATE TABLE identity_service_accounts (
    id varchar(64) NOT NULL,
    client_id varchar(128) NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    secret_hash text NOT NULL,
    status varchar(32) NOT NULL,
    expires_at timestamp(6) NULL,
    last_used_at timestamp(6) NULL,
	failed_attempts bigint NOT NULL DEFAULT 0,
	locked_until timestamp(6) NULL,
    created_at timestamp(6) NOT NULL,
    created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL,
    deleted_at timestamp(6) NULL,
    deleted_by text NULL,
	PRIMARY KEY (id),
    CHECK (status IN ('active', 'disabled')),
    CHECK (version > 0)
);

CREATE UNIQUE INDEX identity_service_accounts_client_unique
	ON identity_service_accounts (client_id);
CREATE INDEX identity_service_accounts_status_created_idx
	ON identity_service_accounts (status, created_at DESC);
