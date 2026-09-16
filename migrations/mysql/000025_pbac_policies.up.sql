CREATE TABLE pbac_policies (
    id varchar(64) PRIMARY KEY,
    code varchar(128) NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    scope varchar(16) NOT NULL,
    tenant_id varchar(128) NULL,
    published_version_number bigint NULL,
    status varchar(16) NOT NULL,
    active_identity varchar(320) GENERATED ALWAYS AS (
        CASE WHEN deleted_at IS NULL THEN CONCAT(scope, ':', COALESCE(tenant_id, ''), ':', LOWER(code)) ELSE NULL END
    ) STORED,
    created_at timestamp(6) NOT NULL, created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text,
    UNIQUE KEY pbac_policies_identity_unique(active_identity),
    KEY pbac_policies_published_idx(status,scope,tenant_id,published_version_number),
    CHECK(scope IN('global','tenant')),
    CHECK(status IN('active','disabled')),
    CHECK(published_version_number IS NULL OR published_version_number > 0),
    CHECK((scope='global' AND tenant_id IS NULL) OR (scope='tenant' AND tenant_id IS NOT NULL AND tenant_id<>''))
);

CREATE TABLE pbac_policy_versions (
    id varchar(64) PRIMARY KEY,
    policy_id varchar(64) NOT NULL,
    version_number bigint NOT NULL,
    document longtext NOT NULL,
    status varchar(16) NOT NULL,
    published_at timestamp(6) NULL,
    published_by text,
    active_version_number bigint GENERATED ALWAYS AS (
        CASE WHEN deleted_at IS NULL THEN version_number ELSE NULL END
    ) STORED,
    active_published_policy_id varchar(64) GENERATED ALWAYS AS (
        CASE WHEN deleted_at IS NULL AND status='published' THEN policy_id ELSE NULL END
    ) STORED,
    created_at timestamp(6) NOT NULL, created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text,
    UNIQUE KEY pbac_policy_versions_number_unique(policy_id,active_version_number),
    UNIQUE KEY pbac_policy_versions_published_unique(active_published_policy_id),
    KEY pbac_policy_versions_status_idx(policy_id,status,version_number),
    CONSTRAINT pbac_policy_versions_policy_fk FOREIGN KEY(policy_id) REFERENCES pbac_policies(id),
    CHECK(version_number > 0),
    CHECK(status IN('draft','published','archived')),
    CHECK((status='draft' AND published_at IS NULL AND published_by IS NULL) OR (status IN('published','archived') AND published_at IS NOT NULL AND published_by IS NOT NULL))
);

CREATE TABLE pbac_policy_actions (
    id varchar(64) PRIMARY KEY,
    policy_version_id varchar(64) NOT NULL,
    resource varchar(128) NOT NULL,
    action varchar(64) NOT NULL,
    created_at timestamp(6) NOT NULL, created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text,
    UNIQUE KEY pbac_policy_actions_unique(policy_version_id,resource,action),
    KEY pbac_policy_actions_lookup_idx(resource,action,policy_version_id),
    CONSTRAINT pbac_policy_actions_version_fk FOREIGN KEY(policy_version_id) REFERENCES pbac_policy_versions(id)
);

CREATE TRIGGER pbac_policies_audit_bi BEFORE INSERT ON pbac_policies FOR EACH ROW SET NEW.created_at=CURRENT_TIMESTAMP(6),NEW.created_by=NULLIF(TRIM(@app_actor_id),''),NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=1;
CREATE TRIGGER pbac_policies_audit_bu BEFORE UPDATE ON pbac_policies FOR EACH ROW SET NEW.created_at=OLD.created_at,NEW.created_by=OLD.created_by,NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=OLD.version+1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL WHEN OLD.deleted_at IS NULL THEN NULLIF(TRIM(@app_actor_id),'') ELSE OLD.deleted_by END;
CREATE TRIGGER pbac_policies_audit_bd BEFORE DELETE ON pbac_policies FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='physical delete is forbidden';
CREATE TRIGGER pbac_policy_versions_audit_bi BEFORE INSERT ON pbac_policy_versions FOR EACH ROW SET NEW.created_at=CURRENT_TIMESTAMP(6),NEW.created_by=NULLIF(TRIM(@app_actor_id),''),NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=1;
CREATE TRIGGER pbac_policy_versions_audit_bu BEFORE UPDATE ON pbac_policy_versions FOR EACH ROW SET NEW.created_at=OLD.created_at,NEW.created_by=OLD.created_by,NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=OLD.version+1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL WHEN OLD.deleted_at IS NULL THEN NULLIF(TRIM(@app_actor_id),'') ELSE OLD.deleted_by END;
CREATE TRIGGER pbac_policy_versions_audit_bd BEFORE DELETE ON pbac_policy_versions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='physical delete is forbidden';
CREATE TRIGGER pbac_policy_actions_audit_bi BEFORE INSERT ON pbac_policy_actions FOR EACH ROW SET NEW.created_at=CURRENT_TIMESTAMP(6),NEW.created_by=NULLIF(TRIM(@app_actor_id),''),NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=1;
CREATE TRIGGER pbac_policy_actions_audit_bu BEFORE UPDATE ON pbac_policy_actions FOR EACH ROW SET NEW.created_at=OLD.created_at,NEW.created_by=OLD.created_by,NEW.updated_at=CURRENT_TIMESTAMP(6),NEW.updated_by=NULLIF(TRIM(@app_actor_id),''),NEW.version=OLD.version+1,NEW.deleted_by=CASE WHEN NEW.deleted_at IS NULL THEN NULL WHEN OLD.deleted_at IS NULL THEN NULLIF(TRIM(@app_actor_id),'') ELSE OLD.deleted_by END;
CREATE TRIGGER pbac_policy_actions_audit_bd BEFORE DELETE ON pbac_policy_actions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='physical delete is forbidden';
