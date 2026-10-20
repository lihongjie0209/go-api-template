# Resource/Action 与权限授权迁移

操作授权不再执行策略表达式。业务代码声明稳定的 `resource:action`；启动时以代码为准覆盖 `source=code` 的资源和动作，并停用代码中已删除的项。`source=manual` 由管理接口维护，启动同步绝不修改。资源 key、scope 和动作 key 创建后不可修改；名称、描述、状态可按版本号修改。

权限树是供角色和租户授权使用的业务集合。一个权限叶子可关联多个同一 scope 的动作；动作本身不是权限。平台主体通过显式平台授权获得平台权限，租户成员必须同时满足租户授权上限，以及管理员或角色授权。当前主体范围的资料、会话等操作由认证与服务层对象归属校验。数据权限是独立的 SQL/对象范围，操作授权不替代租户隔离或数据权限。

| 关注点 | 决定 |
| --- | --- |
| 契约 | 资源、动作、平台主体授权分别提供 POST JSON create/get/page/update/delete；权限动作关联提供 get/set。动作 page 的 `keyword` 不区分大小写匹配动作标识/名称及所属资源标识/名称，可与资源 ID、scope、status 等精确条件叠加；结果与 count 使用相同条件。page 默认 1/20、最大 200，ID IN 最大 200，动作关联最大 100；排序使用固定 key 和 ID。统一响应 envelope 与全局错误码。 |
| 认证授权 | 所有管理接口需 JWT；资源/动作分别使用 `authorization.resource` / `authorization.action`，平台授权使用 `authorization.platform-grant`，权限关联使用 `permission.definition`。未声明的接口启动失败。 |
| 操作/安全日志 | 所有写操作在事务内记录操作日志与权限变更安全日志；失败记录安全摘要，不记录凭据。查询默认不记录。 |
| 缓存 | 暂不缓存操作授权；授权表查询是权威结果，避免跨实例失效窗口。请求内租户授权结果复用。 |
| 分布式锁 | 不使用；唯一约束、事务、版本号是并发正确性边界。 |
| 乐观锁 | update/delete/set 均要求 `version`，SQL 条件包含预期版本；冲突返回统一冲突码。 |
| 审计 | 新表全部具备时间、操作者、版本、逻辑删除字段；事务内 actor 注入数据库触发器。 |
| 展示 | API 时间为 RFC 3339 偏移格式；动作 create/get/page/update 均返回所属资源 ID、稳定标识和展示名称 `resource_name`，以同库 JOIN 读取，不额外请求资源列表；权限动作关联返回资源/动作名称。平台授权的 create/get/page/update 返回主体 ID、显示名和独立的登录标识（用户 `username` / 服务账号 `client_id`），主体被删除时回退至 ID；前端列表同时展示名称和标识，避免同名主体混淆。只读字段不改变授权键和权限判定。 |
| 测试 | 单测覆盖命名、来源保护、授权允许/拒绝/故障与参数边界；Testcontainers 覆盖资源/动作 CRUD、权限多动作关联和平台授权生命周期。 |
| 共享组件 | 使用 Transactor、日志 Recorder、分页和稳定 ID helper；不复制这些实现。 |
| 字典 | 授权目录是安全敏感的管理数据，不作为公共匿名数据字典。 |

## 迁移与引导

迁移 `000040` 创建权限动作关联和平台主体授权表，并删除旧操作策略表。启动同步先更新代码资源目录，再一次性将旧 `permissions.resource/action` 关联迁入新表。它只处理旧记录一次，不会在管理员修改关联后恢复已移除的关联。旧发布策略的判断结果不会转换为授权；升级前必须审计并重新授予所需权限。未授权操作会 fail closed。

首次平台管理员的授权必须通过受控数据库引导流程或部署系统的一次性作业完成；不能开放匿名授权接口，也不能让“第一个注册用户”自动成为管理员。引导时使用 `app.actor_id`，为管理员创建平台权限及对应 Resource/Action 关联，再写入 `platform_principal_permissions`。之后通过受保护的管理 API 维护。此引导操作需纳入变更记录；数据库快照和旧策略导出必须在执行破坏性迁移前完成。

资源与动作接口：`/api/v1/authorization/resources/{create,get,page,update,delete}` 与 `/api/v1/authorization/actions/{create,get,page,update,delete}`。权限关联：`/api/v1/permissions/actions/{get,set}`。平台授权：`/api/v1/platform/permission-grants/{create,get,page,update,delete}`。平台授权的可选权限使用 `/api/v1/platform/permission-grants/permissions/page`，根据关联动作而非权限主资源过滤，详见 [候选接口设计](platform-grant-permission-options.md)。
