SET @app_actor_id = 'system:migration';
UPDATE pbac_policy_actions SET deleted_at=CURRENT_TIMESTAMP(6) WHERE id='b49c52be-fec0-5451-aebd-39a42608b396' AND deleted_at IS NULL;
UPDATE pbac_policy_versions SET deleted_at=CURRENT_TIMESTAMP(6) WHERE id='fce46181-1293-5d4d-adb8-2adb9157b20e' AND deleted_at IS NULL;
UPDATE pbac_policies SET deleted_at=CURRENT_TIMESTAMP(6) WHERE id='4fbc3f70-cc41-589c-bcdd-4a79896f901b' AND deleted_at IS NULL;
UPDATE permissions SET deleted_at=CURRENT_TIMESTAMP(6) WHERE id='113ab186-bba8-5d22-b37f-f1f65bb4b211' AND deleted_at IS NULL;
SET @app_actor_id = NULL;
