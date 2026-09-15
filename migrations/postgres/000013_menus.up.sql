CREATE TABLE menus (
    id text PRIMARY KEY, parent_id text REFERENCES menus(id), menu_key text NOT NULL, name text NOT NULL,
    menu_type text NOT NULL CHECK(menu_type IN('directory','page','button','external')),
    route_path text NOT NULL DEFAULT '', component text NOT NULL DEFAULT '', external_url text NOT NULL DEFAULT '', icon text NOT NULL DEFAULT '',
    permission_id text REFERENCES permissions(id), visible boolean NOT NULL DEFAULT true,
    status text NOT NULL CHECK(status IN('active','disabled')), sort_order bigint NOT NULL DEFAULT 0, metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL, created_by text NOT NULL, updated_at timestamptz NOT NULL, updated_by text NOT NULL,
    version bigint NOT NULL CHECK(version>0), deleted_at timestamptz, deleted_by text
);
CREATE UNIQUE INDEX menus_key_unique ON menus(lower(menu_key)) WHERE deleted_at IS NULL;
CREATE INDEX menus_parent_idx ON menus(parent_id,sort_order) WHERE deleted_at IS NULL;
CREATE INDEX menus_permission_idx ON menus(permission_id) WHERE deleted_at IS NULL AND permission_id IS NOT NULL;
SELECT app_enable_audit('menus');
