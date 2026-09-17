# Authorization Policy Specification v1

本文档定义单体应用内操作权限（Action Authorization）的策略语义。数据权限已拆分到独立的 [Data Permission Policy Specification](./data-permission-policy-spec.md)，两类策略不得混用。

## 1. 职责边界

操作权限只回答“主体是否可以对某类资源执行某个操作”：

```text
Decision = Evaluate(Subject, ResourceType, Action, TrustedOperationContext) -> Allow | Deny | Indeterminate
```

- `subject`：谁在操作；
- `resource.type`：操作哪一类业务资源；
- `actions`：执行什么操作；
- `when`：可选的可信操作上下文条件；
- `effect`：匹配后产生 Allow 或 Deny。

操作权限策略不包含数据 `condition`。本人数据、所属部门、数据所有者、负责范围和拟修改字段等条件属于数据权限模块。`when` 只能使用服务端构造的主体、租户、接口、认证和环境属性。

## 2. 策略格式

```yaml
api_version: authorization.platform/v1
kind: ActionPolicy
metadata:
  code: tenant-admin-maintain-member
  name: 租户管理员维护成员
scope:
  type: tenant
  tenant_id: tenant-001
spec:
  subject:
    authenticated: true
    types: [user]
    roles:
      any_of: [tenant_admin]
  resource:
    type: tenant.member
  actions: [create, read, list, update, remove]
  when: environment.business_day && environment.local_hour >= 9 && environment.local_hour < 18
  effect: allow
```

策略正文只允许 `api_version`、`kind`、`metadata`、`scope` 和上述 `spec` 字段。未知字段必须在保存或发布时拒绝。

### 2.1 操作条件设计决策

| Concern | Decision and rationale |
| --- | --- |
| Contract | `spec.when` 是最长 4096 字节、结果必须为 boolean 的受限 CEL；空值表示只使用结构化 Matcher。错误使用稳定的策略校验/求值失败分类。 |
| Authentication | 条件只能读取已经验证的认证方式，不能读取 Authorization、Cookie、Token 或原始 Header。 |
| Authorization | 允许根固定为 `subject`、`tenant`、`request`、`environment`、`authentication`；禁止 `resource`、`proposed`、请求 body、动态字段、函数和外部调用。 |
| Operation/Security log | 纯求值不单独记录；策略生命周期继续记录操作和安全日志，但不记录表达式输入值。 |
| Cache | 发布时编译并随不可变策略快照原子替换；请求期不重新解析表达式。 |
| Distributed/optimistic lock | 纯求值无锁；发布生命周期继续使用数据库行锁和策略版本乐观锁。 |
| Audit | 时间、环境、接口和认证属性全部由服务端上下文生成，客户端不能覆盖。 |
| Presentation | 拒绝响应只暴露稳定错误码，不返回命中条件、属性值或策略正文。 |
| Tests | 覆盖工作时间边界、条件不匹配、Deny 优先、未知字段、`resource/proposed` 越界、函数、复杂度、取消和求值错误。 |
| Shared capability | 复用现有 CEL 依赖和 PBAC 快照；条件编译器保持在 PBAC 公共模块，稳定后可提取到平台 SDK。 |
| Dictionary | 策略是安全配置且可能包含主体匹配信息，不作为数据字典提供者。 |

## 3. Subject Matcher

| 字段 | 语义 |
| --- | --- |
| `authenticated` | 主体认证状态必须相同 |
| `types` | 命中至少一个主体类型 |
| `ids` | 命中至少一个主体 ID |
| `roles.any_of` | 至少拥有一个角色 |
| `roles.all_of` | 拥有全部角色 |
| `roles.none_of` | 不拥有任何列出的角色 |

不同字段之间使用 AND。未声明字段不参与匹配。空数组和未知字段必须拒绝。

## 4. Resource/Action

Resource 和 Action 必须存在于启动时冻结的注册表中。规范标识为 `<resource>:<action>`，例如 `tenant.member:update`。Resource scope 分为 `platform`、`tenant`、`principal`：平台和当前主体资源只能由全局策略授权；租户策略只能授权相同租户下的 tenant Resource。principal Resource 由全局策略决定是否允许该类主体调用，再由业务 Service 强制当前主体所有权，因此无需先选择租户。

## 5. 多策略结果

操作权限固定采用 deny-overrides：

| 匹配结果 | 最终结果 |
| --- | --- |
| 至少一个 Deny | Deny |
| 没有 Deny，至少一个 Allow | Allow |
| 没有匹配策略 | Deny |
| 任一必要求值为 Indeterminate | Indeterminate，调用方按 Deny 执行 |

策略顺序、创建时间和发布时间不得影响结果。首版不支持数字优先级。

结构化 Subject/Resource/Action 匹配后才计算 `when`。`when=false` 表示该策略未匹配，不产生 Deny；一个匹配且 `when=true` 的 Deny 仍覆盖全部 Allow。任何已发布候选策略发生求值错误时结果为 Indeterminate 并 fail-closed，不能跳过坏策略继续授权。

### 5.1 `when` 属性契约

允许的服务端可信属性为：

- `subject.id/type/authenticated/tenant_id/membership_id/roles`；
- `tenant.id`；
- `request.transport/operation`；
- `environment.profile/timezone/local_hour/weekday/business_day`；
- `authentication.scheme`。

`weekday` 使用 ISO 8601 的 1（周一）到 7（周日），平台时间以 `Asia/Shanghai` 生成；一次决策只捕获一次时间。当前 `business_day` 仅表示周一到周五，不包含法定节假日，未来接入可信工作日日历时保持字段语义并增加日历版本。支持 boolean 逻辑、相等/不等、有序比较、`in` 字面量集合和括号；不支持任何函数、方法、动态索引或自定义变量。

## 6. 执行位置

HTTP/gRPC 拦截器或 Service 命令入口先执行操作权限判断。Allow 只表示可以发起该类操作，不表示可以访问该资源类型下的全部数据。

```text
认证
  -> 操作权限
  -> 数据权限范围
  -> Repository 租户隔离 + 数据范围 + 业务查询条件
  -> 乐观锁/必要的最小粒度分布式锁
  -> 数据库操作
```

### 6.1 接口授权声明

每个 HTTP 路由和 gRPC 方法必须在代码注册时声明一个不可缺省的接口授权描述：

```go
type EndpointAuthorization struct {
    Authentication AuthenticationMode
    Resource       string
    Action         string
    DataPermission DataPermissionMode
}
```

- 业务接口必须声明已注册的 `resource + action`；
- 匿名接口必须显式声明 `public`，不能用缺少策略表示公开；
- 仅认证、不需要业务权限的接口也必须使用显式的受控模式，不能留空；
- 内部接口必须显式声明内部身份方式，例如 PSK 或服务身份；
- 操作具体数据的接口声明 `DataPermissionRequired`；纯创建命令根据待创建对象执行单条数据 Predicate；
- 健康检查、指标等运维端点使用独立运维安全契约，不进入业务 Resource/Action。

路由注册、接口授权描述和 Handler 必须在同一个代码调用中绑定，避免先注册路由后遗漏授权元数据。启动时校验实际路由/RPC 与接口授权注册表完全一致；缺失、重复、未知 Resource/Action 或不合法的 public 声明都阻止启动。CI 同时比较 HTTP/gRPC、OpenAPI/Protobuf 和接口授权注册表。

### 6.2 拦截与数据权限边界

统一 HTTP 中间件和 gRPC Interceptor 根据当前接口授权描述执行操作权限引擎。Handler 不再各自编写权限判断。操作权限通过后：

- `DataPermissionNone`：接口不读取或修改受数据范围控制的业务记录；
- `DataPermissionRequired`：Service 必须构建 DataScope，并传入 Repository；
- `DataPermissionObject`：create 等命令对服务端构造的目标对象执行内存 Predicate。

数据权限不能只在 HTTP/gRPC 拦截器处理，因为拦截器没有可靠的目标数据属性，也不能替代 Repository 的 SQL 范围。

受保护接口如果声明 `data_permission=none`，必须同时在 Endpoint 描述符中给出不超过 256 字节的审计理由，例如平台级资源、当前主体自有数据、服务内固有租户边界或尚不存在目标行的创建操作。`required/object` 不允许携带豁免理由。未填写理由、未知 Resource/Action 或 Required 资源没有查询 Schema 都会导致服务启动失败。

### 6.3 租户角色授权与显式策略的合并语义

`permissions` 叶子节点必须映射到代码注册表中的 Resource/Action。租户授权上限和租户角色授权是系统维护的 Allow 来源，不是另一套权限词汇：

1. 先计算已发布 ActionPolicy；任意匹配的显式 Deny 立即拒绝；
2. 已发布策略显式 Allow 时允许；
3. 仅当结果为 `PBAC_NO_MATCHING_POLICY` 且 Resource 为 tenant scope 时，才检查当前有效租户、成员、租户授权上限，以及管理员或有效角色授权；
4. 两类来源都未允许时拒绝，查询失败时返回不可判定并 fail-closed。

平台 scope 的 Resource/Action 禁止写入租户授权上限和租户角色。接口描述声明的 Scope 必须与代码注册表一致，否则视为配置错误而不是尝试降级授权。这样菜单可见性和后端接口使用同一组有效权限，同时保留显式 Deny 的紧急封禁及例外控制能力。

平台管理能力与租户/当前主体能力必须使用不同 Resource。当前平台管理使用 `tenant:*`、`menu:*`，当前租户资料和菜单分别使用 tenant scope 的 `tenant.profile:*`、`menu.current:read`；不得为了复用 Handler 把租户自治接口绑定到平台 Resource，否则租户策略和租户角色将无法安全授权该接口。

租户选择发生在 tenant context 建立之前，因此“可用租户”和“切换租户”使用 principal scope 的 `tenant.selection:list/switch`，Service 再校验当前用户、会话及目标成员关系。已经进入租户后的上下文读取使用 tenant scope 的 `tenant.context:read`。

设置租户权限上限是平台命令，使用 `/platform/tenant-authorization/permissions/set` 与 `tenant:grant`。设置管理员提供两个明确入口：平台入口使用 `tenant:assign-administrator`，租户内入口使用 `tenant.authorization:assign-administrator`。同一个 Service 可以复用，但接口的授权范围不得根据请求体动态猜测。

### 6.4 旧路由策略模块退役

旧的路径策略表已经退出运行时。数据库策略只关联稳定的 Resource/Action，不关联 URL、Gin Handler 名称或 gRPC 路径。

退役顺序固定为：

1. 建立接口授权描述和统一拦截器；
2. 为所有 HTTP/gRPC 接口补齐显式声明并通过启动/CI 完整性检查；
3. 将现有路由表达式迁移为操作权限策略；
4. 灰度对比新旧决策并检查拒绝差异；
5. 切换到新操作权限引擎；
6. 删除旧中间件、后台刷新任务、表和迁移配置。

在第 2～5 步完成前不得提前移除旧拦截器，以免出现未保护接口。

## 7. 生命周期

- 策略使用草稿、发布、归档生命周期；运行时只读取已发布版本。
- 创建版本、发布和启停必须使用逻辑策略版本号做乐观锁。
- 数据库是权威来源，运行时使用完整不可变快照。
- 发布后构建完整新快照，全部校验成功后原子替换旧快照。
- 全局策略和租户策略必须使用独立租户索引；不得加载其他租户的策略。
- 变更必须记录操作日志和安全日志，正文及敏感主体属性不得写日志。

## 8. 管理接口边界

- 全局操作策略使用 `/api/v1/pbac/global-policies/...`，绑定平台资源 `pbac.global-policy`；
- 租户操作策略使用 `/api/v1/pbac/tenant-policies/...`，绑定租户资源 `pbac.tenant-policy`；
- 两套路由共用持久化生命周期实现，但 Handler 强制策略文档、目标策略和路由 Scope 一致；
- 数据权限策略使用独立模型、表、引擎与管理接口，不能通过操作策略接口保存；
- 操作策略与数据权限策略分别维护不可变进程内快照；Redis Pub/Sub 仅作为刷新提示，数据库 revision 轮询负责丢消息与断线恢复；
- 项目不兼容旧的混合 Condition 格式，解析时把 `condition` 视为未知字段并拒绝；操作条件只能使用 `when`。

### 8.1 首条策略引导

新数据库没有已发布策略时，所有受保护接口按设计 fail-closed。运维人员使用镜像内的 `pbacctl` 通过数据库连接创建并立即发布首个 ActionPolicy，避免把策略管理接口临时公开：

```bash
/app/pbacctl action-policy publish \
  --config /app/config/config.yaml \
  --env production \
  --actor bootstrap-operator \
  --file /run/secrets/bootstrap-action-policy.yaml
```

租户策略还必须传递与文档 `scope.tenant_id` 完全一致的 `--tenant-id`。命令只支持首次创建发布；重复 code 会由数据库唯一约束拒绝，后续版本必须通过受 PBAC 保护的管理 API 操作。策略文件和数据库凭据由部署系统以 Secret 提供，不能打入镜像或仓库。该引导操作仍通过数据库审计触发器记录 actor，但在操作/安全日志链路尚未建立前不会伪造异步日志。
