DROP INDEX tenant_memberships_user_idx;
CREATE INDEX tenant_memberships_selection_idx
    ON tenant_memberships (user_id, status, tenant_id)
    WHERE deleted_at IS NULL;
CREATE INDEX tenant_memberships_rejoin_idx
    ON tenant_memberships (tenant_id, user_id, deleted_at DESC, id DESC)
    WHERE deleted_at IS NOT NULL;
