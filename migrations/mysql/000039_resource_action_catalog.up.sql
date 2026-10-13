CREATE TABLE authorization_resources (
    id varchar(64) PRIMARY KEY,
    resource_key varchar(128) NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL,
    scope varchar(32) NOT NULL,
    source varchar(32) NOT NULL,
    status varchar(32) NOT NULL,
    created_at timestamp(6) NOT NULL,
    created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL,
    deleted_at timestamp(6) NULL,
    deleted_by text,
    KEY authorization_resources_source_idx (source,status,resource_key)
);
CREATE TRIGGER authorization_resources_audit_bi BEFORE INSERT ON authorization_resources FOR EACH ROW SET NEW.created_at=CURRENT_TIMESTAMP(6),NEW.created_by=NULLIF(TRIM(@app_actor_id),''),NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL ELSE NULLIF(TRIM(@app_actor_id),'') END;
CREATE TRIGGER authorization_resources_audit_bu BEFORE UPDATE ON authorization_resources FOR EACH ROW SET NEW.created_at=OLD.created_at,NEW.created_by=OLD.created_by,NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=OLD.version+1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL WHEN OLD.deleted_at IS NULL THEN NULLIF(TRIM(@app_actor_id),'') ELSE OLD.deleted_by END;
CREATE TRIGGER authorization_resources_audit_bd BEFORE DELETE ON authorization_resources FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='physical delete is forbidden';

CREATE TABLE authorization_resource_actions (
    id varchar(64) PRIMARY KEY,
    resource_id varchar(64) NOT NULL,
    action_key varchar(64) NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    source varchar(32) NOT NULL,
    status varchar(32) NOT NULL,
    created_at timestamp(6) NOT NULL,
    created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL,
    deleted_at timestamp(6) NULL,
    deleted_by text,
    UNIQUE KEY authorization_resource_actions_key_unique (resource_id,action_key),
    KEY authorization_resource_actions_source_idx (source,status,resource_id),
    CONSTRAINT authorization_resource_actions_resource_fk FOREIGN KEY (resource_id) REFERENCES authorization_resources(id)
);
CREATE TRIGGER authorization_resource_actions_audit_bi BEFORE INSERT ON authorization_resource_actions FOR EACH ROW SET NEW.created_at=CURRENT_TIMESTAMP(6),NEW.created_by=NULLIF(TRIM(@app_actor_id),''),NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL ELSE NULLIF(TRIM(@app_actor_id),'') END;
CREATE TRIGGER authorization_resource_actions_audit_bu BEFORE UPDATE ON authorization_resource_actions FOR EACH ROW SET NEW.created_at=OLD.created_at,NEW.created_by=OLD.created_by,NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=OLD.version+1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL WHEN OLD.deleted_at IS NULL THEN NULLIF(TRIM(@app_actor_id),'') ELSE OLD.deleted_by END;
CREATE TRIGGER authorization_resource_actions_audit_bd BEFORE DELETE ON authorization_resource_actions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='physical delete is forbidden';

CREATE TABLE authorization_catalog_syncs (
    source varchar(32) PRIMARY KEY,
    build_time text NOT NULL,
    commit_id text NOT NULL,
    created_at timestamp(6) NOT NULL,
    created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL,
    deleted_at timestamp(6) NULL,
    deleted_by text
);
CREATE TRIGGER authorization_catalog_syncs_audit_bi BEFORE INSERT ON authorization_catalog_syncs FOR EACH ROW SET NEW.created_at=CURRENT_TIMESTAMP(6),NEW.created_by=NULLIF(TRIM(@app_actor_id),''),NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL ELSE NULLIF(TRIM(@app_actor_id),'') END;
CREATE TRIGGER authorization_catalog_syncs_audit_bu BEFORE UPDATE ON authorization_catalog_syncs FOR EACH ROW SET NEW.created_at=OLD.created_at,NEW.created_by=OLD.created_by,NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=OLD.version+1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL WHEN OLD.deleted_at IS NULL THEN NULLIF(TRIM(@app_actor_id),'') ELSE OLD.deleted_by END;
CREATE TRIGGER authorization_catalog_syncs_audit_bd BEFORE DELETE ON authorization_catalog_syncs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='physical delete is forbidden';
SET @app_actor_id = 'system:migration';
INSERT INTO authorization_catalog_syncs (source,build_time,commit_id,created_at,created_by,updated_at,updated_by,version)
VALUES ('code','','',CURRENT_TIMESTAMP(6),'system:migration',CURRENT_TIMESTAMP(6),'system:migration',1);
SET @app_actor_id = NULL;
