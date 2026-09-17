# Data Permission Policy Specification v1

本文档定义数据权限模块的策略格式、多个匹配策略的合并规则以及 Repository SQL 适配契约。数据权限只决定已经通过操作权限检查的主体可以访问哪些数据。

## 1. 决策模型

```text
DataScope = Build(Subject, ResourceType, Action, PublishedPolicies)
FinalScope = (Allow1 OR Allow2 OR ...) AND NOT (Deny1 OR Deny2 OR ...)
```

输出不是接口级 Allow/Deny，而是可应用于单条对象判断和数据库查询的类型化 Predicate。固定规则：

- 多个 Allow 数据范围取并集；
- 多个 Deny 数据范围取并集；
- Deny 范围从 Allow 范围中排除；
- 没有任何匹配的 Allow 时结果为 `FALSE`；
- 空 Allow Condition 等价于允许当前 Resource/Action 的全部数据；
- 空 Deny Condition 等价于拒绝当前 Resource/Action 的全部数据；
- 缺少必要属性、无法编译或无法安全转换 SQL 时 fail-closed。

数据权限合并规则与操作权限的 deny-overrides 不同，因此两者使用独立策略、引擎、快照和持久化表。

## 2. 策略格式

```yaml
api_version: data-permission.platform/v1
kind: DataPermissionPolicy
metadata:
  code: department-manager-member-scope
  name: 部门管理员的成员数据范围
scope:
  type: tenant
  tenant_id: tenant-001
spec:
  subject:
    authenticated: true
    types: [user]
    roles:
      any_of: [department_manager]
  resource: tenant.member
  actions: [read, list, update]
  condition: resource.owner_id == subject.id
  proposed_condition: proposed.status == "active"
  effect: allow
```

`condition` 在本策略中只表示当前数据范围，不能承担接口操作授权。`proposed_condition` 约束服务端校验并构造的创建/更新目标对象，不参与 SQL 拼接。

## 3. Condition 语义

核心输入分为：

- `condition(subject, resource)`：当前持久化行，可转换为 SQL；
- `proposed_condition(subject, proposed)`：服务端构造的目标对象，只进行内存求值。

首轮实现支持 boolean `&&`、`||`、`!`、文本相等 `==`、集合 `resource.field in subject.collection` 和括号。`!=`、有序比较和空值判断只有在对应字段类型、内存语义和 SQL 三者具有一致测试后才能加入；当前发布校验必须拒绝它们。

首版禁止网络、数据库、文件和脚本调用；任意函数；动态 Resource 字段名；策略提供表名、列名、JOIN 或 SQL；以及无法同时生成内存求值器和参数化 SQL 的表达式。

Subject 属性来自认证 Principal、租户成员关系及服务端可信投影。Resource 属性来自 Repository 记录或服务端校验后的待创建对象。客户端提交的 `tenant_id`、`owner_id`、`department_id` 等字段不能直接作为可信权限属性。

创建操作没有旧行，服务端构造的新对象同时作为 `resource` 和 `proposed` 求值。更新操作先使用 `condition` 将当前行范围下推到 Repository，再使用同一条已授权、带租户和版本约束读取出的当前对象以及服务端构造的 `proposed` 对象求值。原始请求 JSON 绝不能直接作为 `proposed`。

带 `proposed_condition` 的 Allow 在 SQL 阶段只下推其当前行 `condition`，随后在状态转换阶段同时满足两者才形成 Allow。带 `proposed_condition` 的 Deny 不得提前作为 SQL Deny 下推，否则会在目标状态未知时过度排除当前行；它在状态转换阶段同时满足当前行和目标对象条件时才参与 deny-overrides。不含目标条件的 Deny 仍直接下推 SQL。

角色与部门投影仅对 `user` 主体生效，并且查询必须同时绑定 `principal.id + tenant_id + membership_id`，校验有效成员记录的 `user_id`。服务账号不能通过携带成员 ID 继承人的角色或部门；如需授权服务账号，必须使用显式 PBAC Subject type/ID 策略。

首版 Subject Schema 是封闭的代码契约：`id`、`tenant_id`、`membership_id` 为 text，`role_codes`、`department_ids` 为 text list。策略发布时拒绝未知 Subject 字段、用 `==` 比较集合或用 `in` 查询标量，避免策略能够发布但在请求期才因属性拼写或类型错误持续 fail-closed。增加可信投影必须同时修改 Subject Schema、服务端解析、规范和测试，不能直接接受请求 JSON 字段。

## 4. 类型化 Predicate IR

Condition 保存或发布时编译为数据库无关的 Predicate IR。IR 只包含 `TRUE`、`FALSE`、`AND`、`OR`、`NOT`、已注册 Resource 字段、Subject 属性参数、受类型约束的常量和批准的比较操作符。原始 CEL 文本不得传给 Repository。

## 5. Resource Query Schema

每个支持数据权限的 Resource 必须由所属业务模块注册查询 Schema：

```go
type Schema struct {
    Resource string
    Fields   map[string]Field
}
type Field struct {
    Column string
    Type   ValueType
}
```

例如首批 `tenant.member` Schema 将 `id`、`owner_id`、`status`、`created_by` 分别映射到代码控制的 `tm` 表别名列。字段和 SQL 列映射是代码契约，应用启动时校验并冻结。未知字段必须在策略发布时拒绝；策略和请求不能提供 SQL 标识符。部门关系等需要 JOIN/EXISTS 的范围必须先增加受控关系字段编译器，不能把 SQL 写进策略。

## 6. SQL 编译契约

```go
type SQLPredicate struct {
    Clause string
    Args   []any
}
```

- Clause 只使用 Schema 注册的 SQL 标识符和编译器生成的运算符；
- Subject 属性和常量全部进入 Args；
- 首层输出使用 `?`，再由 `sqlx.Rebind` 适配 PostgreSQL、MySQL 和 Kingbase；
- 禁止拼接属性值、CEL 文本或客户端输入；
- 输出稳定加括号，不依赖 SQL 隐式优先级。

## 7. Repository 集成

```text
TenantIsolation AND SoftDelete AND BusinessFilters AND DataPermissionScope
```

分页 items 和 `COUNT(*)` 必须复用同一个 DataScope。禁止先分页再内存过滤。

| 操作 | 要求 |
| --- | --- |
| create | 对服务端构造的待创建 Resource 执行内存 Predicate；接入前不得把 Required 范围伪装成已完成 |
| getByID | SQL 同时包含 ID、租户和 DataScope |
| page/list/search | DataScope 在统计和分页前下推 SQL |
| update | 当前目标选择、租户、DataScope 和乐观版本在同一 SQL/事务边界；写入前还必须满足 `proposed_condition` |
| delete | 目标选择、租户、DataScope 和乐观版本在同一 SQL/事务边界 |
| batch | 使用集合 SQL Scope，并校验请求目标与实际命中数量 |

逐条内存过滤只允许用于候选数有严格上限的小集合，不能作为普通分页实现。策略不能声明 JOIN；复杂关系由 Resource 所属模块注册受控字段编译器。

## 8. 错误与安全

Resource/Action 未注册、Schema 缺失、字段未知、Subject 属性缺失或类型不符、Predicate 超限、SQL 编译不支持和快照不可用都必须 fail-closed。日志不得记录 Subject 属性值、完整 Condition 或 SQL 参数值。

## 9. 生命周期与存储

数据权限拥有独立草稿、发布、归档、乐观锁、不可变快照和刷新通知。不能复用操作权限快照，也不能通过同一策略表的 `policy_type` 分支实现。

建议独立表：

```text
data_permission_policies
data_permission_policy_versions
data_permission_policy_actions
```

所有表遵守统一审计、逻辑删除和版本号规范。

## 10. 首轮测试契约

单元测试覆盖 Allow 并集、Deny 并集与排除、无 Allow、空 Condition、属性缺失/类型错误、未知字段、非法列映射、参数顺序和复杂度上限。集成测试使用 `integration` build tag 在 CI 验证 PostgreSQL/MySQL 的分页、统计、getByID、更新和删除范围一致性；本地不启动容器。

## 11. 当前实现状态

| 关注点 | 决策 |
| --- | --- |
| Contract | 提供受限 Condition Parser、类型化 Predicate IR 和参数化 SQL 编译 |
| Authentication | 模块不认证；只接受上层从可信 Principal 构造的 SubjectAttributes |
| Authorization | 调用前必须已通过操作权限；本模块只生成数据范围 |
| Operation/Security log | 纯求值不记录；策略创建、创建版本、发布和启停在同一事务写入操作日志与安全日志，失败尝试独立记录 |
| Cache | 进程内使用原子替换的已发布不可变策略快照；发布后通过环境/服务隔离的 Redis Pub/Sub 低延迟刷新，并由数据库 revision 轮询恢复断线和丢消息 |
| Lock | 纯编译和求值无锁；发布并发使用数据库行锁和乐观锁 |
| Audit/Optimistic lock | 已使用三张独立策略表，包含审计、逻辑删除和版本字段；发布和启停使用行锁与乐观锁 |
| Presentation | SQL 参数和主体属性不返回前端；错误只返回稳定分类 |
| Management API | 已按全局策略和租户策略拆分 POST JSON 管理接口，支持创建、查询、分页、创建版本、发布和启停；读取与变更均按 Principal 的租户边界过滤 |
| Repository | `tenant.member`、`tenant.department`、`tenant.role` 的对象读取、分页/树查询和既有对象变更已下推 SQL；count 与 items 复用同一范围。三类创建接口声明 `DataPermissionObject`，Service 使用生成后的 ID、解析后的关联对象以及服务端确定的状态/actor 构造可信 ResourceAttributes，在写入前使用同一策略快照求值。三类 update 在当前行 SQL 范围命中后，再分别构造 current/proposed 属性并执行 `proposed_condition`；无 Allow 或命中 Deny 均拒绝。 |
| Tests | 单元测试覆盖解析、current/proposed 命名空间隔离、合并、SQL、对象状态转换、租户可见性、持久化发布/启停和 fail-closed；容器集成测试由 CI 验证 PostgreSQL/MySQL 生命周期、成员分页范围和目标状态约束。 |
| Shared capability | Predicate/Schema/Compiler 是跨业务 Resource 的公共基础设施候选，稳定后提取到平台 SDK |
