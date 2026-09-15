CREATE TABLE identity_users (
    id text PRIMARY KEY, username text NOT NULL, display_name text NOT NULL, email text NOT NULL DEFAULT '', phone text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN ('active','disabled','locked','closed')),
    created_at timestamptz NOT NULL, created_by text NOT NULL, updated_at timestamptz NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL CHECK (version > 0), deleted_at timestamptz, deleted_by text
);
CREATE UNIQUE INDEX identity_users_username_unique ON identity_users (lower(username)) WHERE deleted_at IS NULL;
CREATE INDEX identity_users_search_idx ON identity_users (status, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX identity_users_email_idx ON identity_users (lower(email)) WHERE deleted_at IS NULL AND email <> '';
CREATE INDEX identity_users_phone_idx ON identity_users (phone) WHERE deleted_at IS NULL AND phone <> '';
SELECT app_enable_audit('identity_users');
