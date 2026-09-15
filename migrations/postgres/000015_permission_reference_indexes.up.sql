CREATE INDEX tenant_permission_grants_permission_idx
    ON tenant_permission_grants(permission_id) WHERE deleted_at IS NULL;
CREATE INDEX tenant_role_permissions_permission_idx
    ON tenant_role_permissions(permission_id) WHERE deleted_at IS NULL;
CREATE INDEX route_policy_permission_refs_permission_idx
    ON route_policy_permission_refs(permission_id) WHERE deleted_at IS NULL;
CREATE INDEX tenants_owner_user_idx
    ON tenants(owner_user_id) WHERE deleted_at IS NULL;
CREATE INDEX identity_sessions_previous_refresh_idx
    ON identity_sessions(previous_refresh_token_hash)
    WHERE deleted_at IS NULL AND previous_refresh_token_hash <> '';
