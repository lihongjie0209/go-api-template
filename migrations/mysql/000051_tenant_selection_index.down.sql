ALTER TABLE tenant_memberships
    DROP INDEX tenant_memberships_selection_idx,
    ADD INDEX tenant_memberships_user_idx (user_id, status);
