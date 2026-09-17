SET @app_actor_id = 'system:migration';
UPDATE pbac_policy_actions SET deleted_at=CURRENT_TIMESTAMP(6) WHERE id='b7934482-cd1c-5967-bba3-b2da0c363929' AND deleted_at IS NULL;
UPDATE pbac_policy_versions SET deleted_at=CURRENT_TIMESTAMP(6) WHERE id='d28248b4-d7f5-58f0-b4e4-26aa94f0c75c' AND deleted_at IS NULL;
UPDATE pbac_policies SET deleted_at=CURRENT_TIMESTAMP(6) WHERE id='f1014488-7172-5e97-9073-1bdef7fe18bf' AND deleted_at IS NULL;
SET @app_actor_id = NULL;
