ALTER TABLE identity_refresh_token_history
    DROP INDEX identity_refresh_token_history_active_expiry_idx,
    ADD INDEX identity_refresh_token_history_expiry_idx (expires_at, token_hash);
DROP INDEX event_outbox_dead_cleanup_idx ON event_outbox;
DROP INDEX event_outbox_published_cleanup_idx ON event_outbox;
DROP INDEX security_logs_retention_idx ON security_logs;
DROP INDEX operation_logs_retention_idx ON operation_logs;
