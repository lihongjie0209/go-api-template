SELECT set_config('app.actor_id', 'system:migration', false);
UPDATE pbac_policy_actions SET deleted_at=CURRENT_TIMESTAMP WHERE id IN ('97b74493-f271-520a-8f7c-806e56873fa1','8e519fe1-f901-50b7-96d4-db284646a975') AND deleted_at IS NULL;
UPDATE pbac_policy_versions SET deleted_at=CURRENT_TIMESTAMP WHERE id='c3ea4d30-3e91-57a6-a0be-1aeb78273499' AND deleted_at IS NULL;
UPDATE pbac_policies SET deleted_at=CURRENT_TIMESTAMP WHERE id='6a7ff4b6-003e-5709-be92-1d939b4d889b' AND deleted_at IS NULL;
