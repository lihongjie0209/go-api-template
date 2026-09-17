CREATE TABLE tenant_application_grants (
    id varchar(64) PRIMARY KEY,
    tenant_id varchar(64) NOT NULL,
    application_id varchar(64) NOT NULL,
    status varchar(32) NOT NULL,
    starts_at timestamp(6) NULL,
    expires_at timestamp(6) NULL,
    created_at timestamp(6) NOT NULL,
    created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL,
    deleted_at timestamp(6) NULL,
    deleted_by text,
    UNIQUE KEY tenant_application_grants_unique(tenant_id,application_id),
    KEY tenant_application_grants_current_idx(tenant_id,status,application_id),
    KEY tenant_application_grants_application_idx(application_id,tenant_id),
    CONSTRAINT tenant_application_grants_tenant_fk FOREIGN KEY(tenant_id) REFERENCES tenants(id),
    CONSTRAINT tenant_application_grants_application_fk FOREIGN KEY(application_id) REFERENCES applications(id),
    CHECK(status IN ('active','revoked')),
    CHECK(expires_at IS NULL OR starts_at IS NULL OR expires_at > starts_at)
);
CREATE TRIGGER tenant_application_grants_audit_bi BEFORE INSERT ON tenant_application_grants FOR EACH ROW SET NEW.created_at=CURRENT_TIMESTAMP(6),NEW.created_by=NULLIF(TRIM(@app_actor_id),''),NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL ELSE NULLIF(TRIM(@app_actor_id),'') END;
CREATE TRIGGER tenant_application_grants_audit_bu BEFORE UPDATE ON tenant_application_grants FOR EACH ROW SET NEW.created_at=OLD.created_at,NEW.created_by=OLD.created_by,NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=OLD.version+1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL WHEN OLD.deleted_at IS NULL THEN NULLIF(TRIM(@app_actor_id),'') ELSE OLD.deleted_by END;
CREATE TRIGGER tenant_application_grants_audit_bd BEFORE DELETE ON tenant_application_grants FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='physical delete is forbidden';
