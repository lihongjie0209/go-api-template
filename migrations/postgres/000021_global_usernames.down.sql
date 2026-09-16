DROP INDEX identity_users_username_unique;
CREATE UNIQUE INDEX identity_users_username_unique ON identity_users (lower(username)) WHERE deleted_at IS NULL;
