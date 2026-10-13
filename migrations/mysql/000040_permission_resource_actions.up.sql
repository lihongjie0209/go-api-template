CREATE TABLE permission_resource_actions (
    id varchar(128) PRIMARY KEY,
    permission_id varchar(128) NOT NULL,
    action_id varchar(64) NOT NULL,
    created_at timestamp(6) NOT NULL, created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text,
    UNIQUE KEY permission_resource_actions_unique(permission_id,action_id),
    KEY permission_resource_actions_action_idx(action_id),
    CONSTRAINT permission_resource_actions_action_fk FOREIGN KEY(action_id) REFERENCES authorization_resource_actions(id)
);
CREATE TRIGGER permission_resource_actions_audit_bi BEFORE INSERT ON permission_resource_actions FOR EACH ROW SET NEW.created_at=CURRENT_TIMESTAMP(6),NEW.created_by=NULLIF(TRIM(@app_actor_id),''),NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL ELSE NULLIF(TRIM(@app_actor_id),'') END;
CREATE TRIGGER permission_resource_actions_audit_bu BEFORE UPDATE ON permission_resource_actions FOR EACH ROW SET NEW.created_at=OLD.created_at,NEW.created_by=OLD.created_by,NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=OLD.version+1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL WHEN OLD.deleted_at IS NULL THEN NULLIF(TRIM(@app_actor_id),'') ELSE OLD.deleted_by END;
CREATE TRIGGER permission_resource_actions_audit_bd BEFORE DELETE ON permission_resource_actions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='physical delete is forbidden';

CREATE TABLE platform_principal_permissions (
    id varchar(128) PRIMARY KEY,
    principal_type varchar(32) NOT NULL,
    principal_id varchar(128) NOT NULL,
    permission_id varchar(128) NOT NULL,
    created_at timestamp(6) NOT NULL, created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text,
    active_permission_id varchar(128) GENERATED ALWAYS AS (CASE WHEN deleted_at IS NULL THEN permission_id ELSE NULL END) STORED,
    UNIQUE KEY platform_principal_permissions_unique(principal_type,principal_id,active_permission_id),
    KEY platform_principal_permissions_principal_idx(principal_type,principal_id)
);
CREATE TRIGGER platform_principal_permissions_audit_bi BEFORE INSERT ON platform_principal_permissions FOR EACH ROW SET NEW.created_at=CURRENT_TIMESTAMP(6),NEW.created_by=NULLIF(TRIM(@app_actor_id),''),NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL ELSE NULLIF(TRIM(@app_actor_id),'') END;
CREATE TRIGGER platform_principal_permissions_audit_bu BEFORE UPDATE ON platform_principal_permissions FOR EACH ROW SET NEW.created_at=OLD.created_at,NEW.created_by=OLD.created_by,NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=OLD.version+1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL WHEN OLD.deleted_at IS NULL THEN NULLIF(TRIM(@app_actor_id),'') ELSE OLD.deleted_by END;
CREATE TRIGGER platform_principal_permissions_audit_bd BEFORE DELETE ON platform_principal_permissions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='physical delete is forbidden';

DROP TABLE pbac_policy_actions;
DROP TABLE pbac_policy_versions;
DROP TABLE pbac_policies;
