SELECT set_config('app.actor_id', 'system:migration', false);

CREATE TABLE application_session_contexts (
    id text PRIMARY KEY,
    session_id text NOT NULL REFERENCES identity_sessions(id),
    user_id text NOT NULL REFERENCES identity_users(id),
    tenant_id text NOT NULL REFERENCES tenants(id),
    membership_id text NOT NULL REFERENCES tenant_memberships(id),
    application_id text NOT NULL REFERENCES applications(id),
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version bigint NOT NULL CHECK(version > 0),
    deleted_at timestamptz,
    deleted_by text,
    UNIQUE(session_id, tenant_id)
);
CREATE INDEX application_session_contexts_lookup_idx ON application_session_contexts(user_id,tenant_id,membership_id,session_id) WHERE deleted_at IS NULL;
SELECT app_enable_audit('application_session_contexts');

INSERT INTO dictionary_definitions
    (id,dictionary_code,name,dictionary_type,source_type,description,status,extension,created_at,created_by,updated_at,updated_by,version)
VALUES
    ('460e0478-4662-577b-ac5b-8d56ee2b4625','platform.application','应用','enum','provider','平台应用公共展示元数据；不代表租户应用授权','active','{}',CURRENT_TIMESTAMP,'system:migration',CURRENT_TIMESTAMP,'system:migration',1)
ON CONFLICT (id) DO UPDATE SET dictionary_code=EXCLUDED.dictionary_code,name=EXCLUDED.name,dictionary_type='enum',source_type='provider',description=EXCLUDED.description,status='active',extension='{}',deleted_at=NULL,deleted_by=NULL;
