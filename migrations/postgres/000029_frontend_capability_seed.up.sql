SELECT set_config('app.actor_id', 'system:migration', false);

INSERT INTO permissions
    (id,parent_id,permission_key,name,node_type,resource,action,description,sort_order,status,is_system,created_at,created_by,updated_at,updated_by,version)
VALUES
    ('113ab186-bba8-5d22-b37f-f1f65bb4b211',NULL,'authorization.capability.evaluate','查询当前用户前端能力','permission','authorization.capability','evaluate','仅查询当前认证主体的页面和行级能力',1000,'active',true,CURRENT_TIMESTAMP,'system:migration',CURRENT_TIMESTAMP,'system:migration',1)
ON CONFLICT (id) DO UPDATE SET parent_id=NULL,permission_key=EXCLUDED.permission_key,name=EXCLUDED.name,node_type=EXCLUDED.node_type,resource=EXCLUDED.resource,action=EXCLUDED.action,description=EXCLUDED.description,sort_order=EXCLUDED.sort_order,status='active',is_system=true,deleted_at=NULL,deleted_by=NULL;

INSERT INTO pbac_policies
    (id,code,name,description,scope,tenant_id,published_version_number,status,created_at,created_by,updated_at,updated_by,version)
VALUES
    ('4fbc3f70-cc41-589c-bcdd-4a79896f901b','authorization-capability-self-evaluate','当前用户前端能力查询','允许已认证主体查询自己的前端展示能力','global',NULL,1,'active',CURRENT_TIMESTAMP,'system:migration',CURRENT_TIMESTAMP,'system:migration',1)
ON CONFLICT (id) DO UPDATE SET code=EXCLUDED.code,name=EXCLUDED.name,description=EXCLUDED.description,scope='global',tenant_id=NULL,published_version_number=1,status='active',deleted_at=NULL,deleted_by=NULL;

INSERT INTO pbac_policy_versions
    (id,policy_id,version_number,document,status,published_at,published_by,created_at,created_by,updated_at,updated_by,version)
VALUES
    ('fce46181-1293-5d4d-adb8-2adb9157b20e','4fbc3f70-cc41-589c-bcdd-4a79896f901b',1,
'api_version: authorization.platform/v1
kind: ActionPolicy
metadata:
  code: authorization-capability-self-evaluate
  name: 当前用户前端能力查询
  description: 允许已认证主体查询自己的前端展示能力
scope:
  type: global
spec:
  subject:
    authenticated: true
  resource:
    type: authorization.capability
  actions:
    - evaluate
  effect: allow
','published',CURRENT_TIMESTAMP,'system:migration',CURRENT_TIMESTAMP,'system:migration',CURRENT_TIMESTAMP,'system:migration',1)
ON CONFLICT (id) DO UPDATE SET policy_id=EXCLUDED.policy_id,version_number=1,document=EXCLUDED.document,status='published',published_at=CURRENT_TIMESTAMP,published_by='system:migration',deleted_at=NULL,deleted_by=NULL;

INSERT INTO pbac_policy_actions
    (id,policy_version_id,resource,action,created_at,created_by,updated_at,updated_by,version)
VALUES
    ('b49c52be-fec0-5451-aebd-39a42608b396','fce46181-1293-5d4d-adb8-2adb9157b20e','authorization.capability','evaluate',CURRENT_TIMESTAMP,'system:migration',CURRENT_TIMESTAMP,'system:migration',1)
ON CONFLICT (id) DO UPDATE SET policy_version_id=EXCLUDED.policy_version_id,resource=EXCLUDED.resource,action=EXCLUDED.action,deleted_at=NULL,deleted_by=NULL;
