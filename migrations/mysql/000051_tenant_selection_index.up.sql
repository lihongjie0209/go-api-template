ALTER TABLE tenant_memberships
    DROP INDEX tenant_memberships_user_idx,
    ADD INDEX tenant_memberships_selection_idx (user_id, status, deleted_at, tenant_id);
