CREATE TABLE identity_users (
    id varchar(64) NOT NULL PRIMARY KEY, username varchar(255) NOT NULL, display_name text NOT NULL, email text NOT NULL, phone text NOT NULL,
    status varchar(32) NOT NULL, created_at timestamp(6) NOT NULL, created_by text NOT NULL,
    updated_at timestamp(6) NOT NULL, updated_by text NOT NULL, version bigint NOT NULL, deleted_at timestamp(6) NULL, deleted_by text NULL,
    UNIQUE KEY identity_users_username_unique (username), KEY identity_users_search_idx (status, created_at),
    KEY identity_users_email_idx (email(255)), KEY identity_users_phone_idx (phone(64))
);
