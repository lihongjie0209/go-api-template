CREATE TABLE route_definitions (
    id varchar(64) PRIMARY KEY, protocol varchar(16) NOT NULL, method varchar(32) NOT NULL, path text NOT NULL,
    operation text NOT NULL, description text NOT NULL, service_name varchar(255) NOT NULL, source_version text NOT NULL,
    status varchar(32) NOT NULL, last_discovered_at timestamp(6) NOT NULL,
    created_at timestamp(6) NOT NULL, created_by text NOT NULL, updated_at timestamp(6) NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text,
    UNIQUE KEY route_definitions_identity_unique(protocol,method,service_name,path(256)),
    INDEX route_definitions_status_idx(service_name,status,protocol)
);
CREATE TABLE route_policy_definitions (
    id varchar(64) PRIMARY KEY, route_id varchar(64) NOT NULL, expression text NOT NULL, description text NOT NULL,
    priority bigint NOT NULL DEFAULT 0, status varchar(32) NOT NULL,
    created_at timestamp(6) NOT NULL, created_by text NOT NULL, updated_at timestamp(6) NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text,
    UNIQUE KEY route_policy_definitions_route_unique(route_id)
);
CREATE TABLE route_policy_permission_refs (
    id varchar(64) PRIMARY KEY, policy_id varchar(64) NOT NULL, permission_id varchar(64) NOT NULL, scope varchar(32) NOT NULL,
    created_at timestamp(6) NOT NULL, created_by text NOT NULL, updated_at timestamp(6) NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text,
    UNIQUE KEY route_policy_permission_refs_unique(policy_id,permission_id)
);
