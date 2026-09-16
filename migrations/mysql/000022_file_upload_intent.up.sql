CREATE TABLE file_upload_intents (
    id text NOT NULL,
    tenant_id text NOT NULL,
    object_key text NOT NULL,
    status varchar(16) NOT NULL,
    attempts bigint NOT NULL DEFAULT 0,
    last_error text NOT NULL,
    next_attempt_at timestamp(6) NOT NULL,
    created_at timestamp(6) NOT NULL,
    created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL,
    deleted_at timestamp(6) NULL,
    deleted_by text NULL,
    PRIMARY KEY (id(191)),
    UNIQUE KEY file_upload_intents_object_key_unique (object_key(191)),
    KEY file_upload_intents_pending_idx (status, next_attempt_at, id(64))
);

CREATE TRIGGER file_upload_intents_audit_bi BEFORE INSERT ON file_upload_intents FOR EACH ROW SET NEW.created_at = CURRENT_TIMESTAMP(6), NEW.created_by = NULLIF(TRIM(@app_actor_id), ''), NEW.updated_at = CURRENT_TIMESTAMP(6), NEW.updated_by = NULLIF(TRIM(@app_actor_id), ''), NEW.version = 1, NEW.deleted_by = CASE WHEN NEW.deleted_at IS NULL THEN NULL ELSE NULLIF(TRIM(@app_actor_id), '') END;
CREATE TRIGGER file_upload_intents_audit_bu BEFORE UPDATE ON file_upload_intents FOR EACH ROW SET NEW.created_at = OLD.created_at, NEW.created_by = OLD.created_by, NEW.updated_at = CURRENT_TIMESTAMP(6), NEW.updated_by = NULLIF(TRIM(@app_actor_id), ''), NEW.version = OLD.version + 1, NEW.deleted_by = CASE WHEN NEW.deleted_at IS NULL THEN NULL WHEN OLD.deleted_at IS NULL THEN NULLIF(TRIM(@app_actor_id), '') ELSE OLD.deleted_by END;
CREATE TRIGGER file_upload_intents_audit_bd BEFORE DELETE ON file_upload_intents FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'physical delete is forbidden';
