CREATE TABLE tenants (
    id varchar(64) NOT NULL PRIMARY KEY, code varchar(128) NOT NULL UNIQUE, name text NOT NULL,
    description text NOT NULL, status varchar(32) NOT NULL, owner_user_id text NOT NULL, owner_name text NOT NULL,
    created_at timestamp(6) NOT NULL, created_by text NOT NULL, updated_at timestamp(6) NOT NULL,
    updated_by text NOT NULL, version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text NULL
);
CREATE TABLE tenant_departments (
    id varchar(64) NOT NULL PRIMARY KEY, tenant_id varchar(64) NOT NULL, parent_id varchar(64) NULL,
    code varchar(128) NOT NULL, name text NOT NULL, sort_order bigint NOT NULL,
    created_at timestamp(6) NOT NULL, created_by text NOT NULL, updated_at timestamp(6) NOT NULL,
    updated_by text NOT NULL, version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text NULL,
    UNIQUE KEY tenant_departments_code_unique (tenant_id, code),
    KEY tenant_departments_parent_idx (tenant_id, parent_id, sort_order)
);
CREATE TABLE tenant_memberships (
    id varchar(64) NOT NULL PRIMARY KEY, tenant_id varchar(64) NOT NULL, user_id varchar(128) NOT NULL,
    username varchar(255) NOT NULL, display_name text NOT NULL, status varchar(32) NOT NULL, joined_at timestamp(6) NOT NULL,
    created_at timestamp(6) NOT NULL, created_by text NOT NULL, updated_at timestamp(6) NOT NULL,
    updated_by text NOT NULL, version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text NULL,
    UNIQUE KEY tenant_memberships_user_unique (tenant_id, user_id),
    KEY tenant_memberships_user_idx (user_id, status)
);
