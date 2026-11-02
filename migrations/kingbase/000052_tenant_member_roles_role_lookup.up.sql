CREATE INDEX tenant_member_roles_role_lookup_idx
    ON tenant_member_roles (tenant_id, role_id, membership_id)
    WHERE deleted_at IS NULL;
