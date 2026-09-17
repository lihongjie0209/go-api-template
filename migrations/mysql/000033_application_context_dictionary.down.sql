UPDATE dictionary_definitions SET deleted_at=CURRENT_TIMESTAMP(6) WHERE id='460e0478-4662-577b-ac5b-8d56ee2b4625' AND deleted_at IS NULL;
DROP TRIGGER application_session_contexts_audit_bd;
DROP TRIGGER application_session_contexts_audit_bu;
DROP TRIGGER application_session_contexts_audit_bi;
DROP TABLE application_session_contexts;
