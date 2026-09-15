CREATE TABLE permissions (
    id text NOT NULL, parent_id text, permission_key varchar(255) NOT NULL, name text NOT NULL,
    node_type varchar(32) NOT NULL, resource text NOT NULL, action text NOT NULL, description text NOT NULL,
    sort_order bigint NOT NULL DEFAULT 0, status varchar(32) NOT NULL, is_system boolean NOT NULL DEFAULT false,
    created_at timestamp(6) NOT NULL, created_by text NOT NULL, updated_at timestamp(6) NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text,
    PRIMARY KEY (id(255)), UNIQUE KEY permissions_key_unique (permission_key),
    INDEX permissions_tree_idx (parent_id(255), sort_order, id(255))
);
