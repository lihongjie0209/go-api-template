DROP INDEX tenant_memberships_user_idx;
CREATE INDEX tenant_memberships_selection_idx
    ON tenant_memberships (user_id, status, tenant_id)
    WHERE deleted_at IS NULL;
