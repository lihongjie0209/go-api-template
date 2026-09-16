CREATE TABLE event_outbox (
    id varchar(64) PRIMARY KEY,
    subject text NOT NULL,
    envelope longblob NOT NULL,
    trace_parent text NOT NULL,
    trace_state text NOT NULL,
    available_at timestamp(6) NOT NULL,
    attempts bigint NOT NULL DEFAULT 0,
    locked_by text NOT NULL,
    locked_until timestamp(6) NULL,
    published_at timestamp(6) NULL,
    dead_at timestamp(6) NULL,
    last_error text NOT NULL,
    created_at timestamp(6) NOT NULL,
    created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL,
    deleted_at timestamp(6) NULL,
    deleted_by text NULL,
    INDEX event_outbox_pending_idx (published_at, deleted_at, available_at, created_at)
) ENGINE=InnoDB;

CREATE TRIGGER event_outbox_audit_bi BEFORE INSERT ON event_outbox FOR EACH ROW SET NEW.created_at = CURRENT_TIMESTAMP(6), NEW.created_by = NULLIF(TRIM(@app_actor_id), ''), NEW.updated_at = CURRENT_TIMESTAMP(6), NEW.updated_by = NULLIF(TRIM(@app_actor_id), ''), NEW.version = 1, NEW.deleted_by = CASE WHEN NEW.deleted_at IS NULL THEN NULL ELSE NULLIF(TRIM(@app_actor_id), '') END;
CREATE TRIGGER event_outbox_audit_bu BEFORE UPDATE ON event_outbox FOR EACH ROW SET NEW.created_at = OLD.created_at, NEW.created_by = OLD.created_by, NEW.updated_at = CURRENT_TIMESTAMP(6), NEW.updated_by = NULLIF(TRIM(@app_actor_id), ''), NEW.version = OLD.version + 1, NEW.deleted_by = CASE WHEN NEW.deleted_at IS NULL THEN NULL WHEN OLD.deleted_at IS NULL THEN NULLIF(TRIM(@app_actor_id), '') ELSE OLD.deleted_by END;
CREATE TRIGGER event_outbox_audit_bd BEFORE DELETE ON event_outbox FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'physical delete is forbidden';
