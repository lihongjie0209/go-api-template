CREATE INDEX pbac_policies_management_updated_idx
    ON pbac_policies (scope, tenant_id, deleted_at, updated_at DESC, id);
CREATE INDEX pbac_policies_management_created_idx
    ON pbac_policies (scope, tenant_id, deleted_at, created_at, id);
CREATE INDEX data_permission_policies_management_updated_idx
    ON data_permission_policies (scope, tenant_id, deleted_at, updated_at DESC, id);
CREATE INDEX data_permission_policies_management_created_idx
    ON data_permission_policies (scope, tenant_id, deleted_at, created_at, id);
