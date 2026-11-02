CREATE INDEX platform_principal_permissions_permission_idx
    ON platform_principal_permissions(permission_id) WHERE deleted_at IS NULL;
