DROP INDEX tenant_memberships_rejoin_idx;
DROP INDEX tenant_memberships_selection_idx;
CREATE INDEX tenant_memberships_user_idx
    ON tenant_memberships (user_id, status)
    WHERE deleted_at IS NULL;
