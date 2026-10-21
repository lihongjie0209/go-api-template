SELECT set_config('app.actor_id', 'system:migration', false);

UPDATE data_permission_policy_actions a
SET deleted_at = CURRENT_TIMESTAMP
WHERE a.deleted_at IS NULL
  AND EXISTS (
    SELECT 1 FROM data_permission_policy_versions v
    JOIN data_permission_policies p ON p.id = v.policy_id
    WHERE v.id = a.policy_version_id AND p.scope = 'tenant'
      AND NOT EXISTS (SELECT 1 FROM tenants t WHERE t.id = p.tenant_id AND t.deleted_at IS NULL)
  );

UPDATE data_permission_policy_versions v
SET deleted_at = CURRENT_TIMESTAMP
WHERE v.deleted_at IS NULL
  AND EXISTS (
    SELECT 1 FROM data_permission_policies p
    WHERE p.id = v.policy_id AND p.scope = 'tenant'
      AND NOT EXISTS (SELECT 1 FROM tenants t WHERE t.id = p.tenant_id AND t.deleted_at IS NULL)
  );

UPDATE data_permission_policies p
SET deleted_at = CURRENT_TIMESTAMP
WHERE p.deleted_at IS NULL AND p.scope = 'tenant'
  AND NOT EXISTS (SELECT 1 FROM tenants t WHERE t.id = p.tenant_id AND t.deleted_at IS NULL);

SELECT set_config('app.actor_id', '', false);
