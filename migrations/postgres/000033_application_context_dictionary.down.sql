SELECT set_config('app.actor_id', 'system:migration', false);
UPDATE dictionary_definitions SET deleted_at=CURRENT_TIMESTAMP WHERE id='460e0478-4662-577b-ac5b-8d56ee2b4625' AND deleted_at IS NULL;
DROP TABLE application_session_contexts;
