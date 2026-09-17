SELECT set_config('app.actor_id', 'system:migration', false);

INSERT INTO pbac_policies
    (id,code,name,description,scope,tenant_id,published_version_number,status,created_at,created_by,updated_at,updated_by,version)
VALUES
    ('6a7ff4b6-003e-5709-be92-1d939b4d889b','identity-profile-self-service','当前用户资料自助维护','允许用户主体读取和更新自己的资料','global',NULL,1,'active',CURRENT_TIMESTAMP,'system:migration',CURRENT_TIMESTAMP,'system:migration',1)
ON CONFLICT (id) DO UPDATE SET code=EXCLUDED.code,name=EXCLUDED.name,description=EXCLUDED.description,scope='global',tenant_id=NULL,published_version_number=1,status='active',deleted_at=NULL,deleted_by=NULL;

INSERT INTO pbac_policy_versions
    (id,policy_id,version_number,document,status,published_at,published_by,created_at,created_by,updated_at,updated_by,version)
VALUES
    ('c3ea4d30-3e91-57a6-a0be-1aeb78273499','6a7ff4b6-003e-5709-be92-1d939b4d889b',1,
'api_version: authorization.platform/v1
kind: ActionPolicy
metadata:
  code: identity-profile-self-service
  name: 当前用户资料自助维护
  description: 允许用户主体读取和更新自己的资料
scope:
  type: global
spec:
  subject:
    authenticated: true
    types:
      - user
  resource:
    type: identity.profile
  actions:
    - read
    - update
  effect: allow
','published',CURRENT_TIMESTAMP,'system:migration',CURRENT_TIMESTAMP,'system:migration',CURRENT_TIMESTAMP,'system:migration',1)
ON CONFLICT (id) DO UPDATE SET policy_id=EXCLUDED.policy_id,version_number=1,document=EXCLUDED.document,status='published',published_at=CURRENT_TIMESTAMP,published_by='system:migration',deleted_at=NULL,deleted_by=NULL;

INSERT INTO pbac_policy_actions
    (id,policy_version_id,resource,action,created_at,created_by,updated_at,updated_by,version)
VALUES
    ('97b74493-f271-520a-8f7c-806e56873fa1','c3ea4d30-3e91-57a6-a0be-1aeb78273499','identity.profile','read',CURRENT_TIMESTAMP,'system:migration',CURRENT_TIMESTAMP,'system:migration',1),
    ('8e519fe1-f901-50b7-96d4-db284646a975','c3ea4d30-3e91-57a6-a0be-1aeb78273499','identity.profile','update',CURRENT_TIMESTAMP,'system:migration',CURRENT_TIMESTAMP,'system:migration',1)
ON CONFLICT (id) DO UPDATE SET policy_version_id=EXCLUDED.policy_version_id,resource=EXCLUDED.resource,action=EXCLUDED.action,deleted_at=NULL,deleted_by=NULL;
