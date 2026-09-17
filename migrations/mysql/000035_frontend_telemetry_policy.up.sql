SET @app_actor_id = 'system:migration';

INSERT INTO pbac_policies
    (id,code,name,description,scope,tenant_id,published_version_number,status,created_at,created_by,updated_at,updated_by,version)
VALUES
    ('f1014488-7172-5e97-9073-1bdef7fe18bf','frontend-telemetry-self-record','前端行为遥测上报','允许用户主体上报自己的菜单浏览和操作事件','global',NULL,1,'active',CURRENT_TIMESTAMP(6),'system:migration',CURRENT_TIMESTAMP(6),'system:migration',1)
ON DUPLICATE KEY UPDATE code='frontend-telemetry-self-record',name='前端行为遥测上报',description='允许用户主体上报自己的菜单浏览和操作事件',scope='global',tenant_id=NULL,published_version_number=1,status='active',deleted_at=NULL,deleted_by=NULL;

INSERT INTO pbac_policy_versions
    (id,policy_id,version_number,document,status,published_at,published_by,created_at,created_by,updated_at,updated_by,version)
VALUES
    ('d28248b4-d7f5-58f0-b4e4-26aa94f0c75c','f1014488-7172-5e97-9073-1bdef7fe18bf',1,
'api_version: authorization.platform/v1
kind: ActionPolicy
metadata:
  code: frontend-telemetry-self-record
  name: 前端行为遥测上报
  description: 允许用户主体上报自己的菜单浏览和操作事件
scope:
  type: global
spec:
  subject:
    authenticated: true
    types:
      - user
  resource:
    type: frontend.telemetry
  actions:
    - record
  effect: allow
','published',CURRENT_TIMESTAMP(6),'system:migration',CURRENT_TIMESTAMP(6),'system:migration',CURRENT_TIMESTAMP(6),'system:migration',1)
ON DUPLICATE KEY UPDATE policy_id='f1014488-7172-5e97-9073-1bdef7fe18bf',version_number=1,document=VALUES(document),status='published',published_at=CURRENT_TIMESTAMP(6),published_by='system:migration',deleted_at=NULL,deleted_by=NULL;

INSERT INTO pbac_policy_actions
    (id,policy_version_id,resource,action,created_at,created_by,updated_at,updated_by,version)
VALUES
    ('b7934482-cd1c-5967-bba3-b2da0c363929','d28248b4-d7f5-58f0-b4e4-26aa94f0c75c','frontend.telemetry','record',CURRENT_TIMESTAMP(6),'system:migration',CURRENT_TIMESTAMP(6),'system:migration',1)
ON DUPLICATE KEY UPDATE policy_version_id=VALUES(policy_version_id),resource=VALUES(resource),action=VALUES(action),deleted_at=NULL,deleted_by=NULL;

SET @app_actor_id = NULL;
