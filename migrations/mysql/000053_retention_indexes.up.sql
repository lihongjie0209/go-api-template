CREATE INDEX operation_logs_retention_idx ON operation_logs (occurred_at, id);
CREATE INDEX security_logs_retention_idx ON security_logs (occurred_at, id);
CREATE INDEX event_outbox_published_cleanup_idx ON event_outbox (deleted_at, published_at, id);
CREATE INDEX event_outbox_dead_cleanup_idx ON event_outbox (deleted_at, dead_at, id);
ALTER TABLE identity_refresh_token_history
    DROP INDEX identity_refresh_token_history_expiry_idx,
    ADD INDEX identity_refresh_token_history_active_expiry_idx (deleted_at, expires_at, token_hash);
