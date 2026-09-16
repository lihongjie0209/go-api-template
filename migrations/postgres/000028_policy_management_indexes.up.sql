CREATE INDEX pbac_policies_management_updated_idx
    ON pbac_policies (scope, tenant_id, updated_at DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX pbac_policies_management_created_idx
    ON pbac_policies (scope, tenant_id, created_at, id) WHERE deleted_at IS NULL;
CREATE INDEX data_permission_policies_management_updated_idx
    ON data_permission_policies (scope, tenant_id, updated_at DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX data_permission_policies_management_created_idx
    ON data_permission_policies (scope, tenant_id, created_at, id) WHERE deleted_at IS NULL;
