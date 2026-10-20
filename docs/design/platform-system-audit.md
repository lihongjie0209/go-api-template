# 平台系统日志查询

平台系统日志专指 `tenant_id = ''` 的后台任务和平台上下文记录，不是跨租户日志总览。原有租户日志接口继续只读取当前租户。平台管理员须先退出租户上下文，并持有对应平台资源授权。系统任务并不保证每次执行都会产生操作或安全日志；任务运行状态仍以 `scheduled_job_runs` 为准。

| 接口决策 | 平台操作日志 get/page | 平台安全日志 get/page |
| --- | --- | --- |
| Contract | `POST /api/v1/platform/operation-logs/{get,page}`；复用 ID/page 请求与统一响应；page 必填半开时间窗，最多 31 天；禁止 `tenant_ids`；其他过滤沿用原接口 | `POST /api/v1/platform/security-logs/{get,page}`；同左，复用安全日志过滤 |
| Authentication | JWT；无匿名或 PSK 通配符 | JWT；无匿名或 PSK 通配符 |
| Authorization | `platform.operation-log:read/list`，平台作用域；服务层还要求无活动租户上下文，SQL 固定 `tenant_id=''` | `platform.security-log:read/list`；同左 |
| Operation log | 成功查询记录 `platform.operation_log.get/page`；只记录 ID、页大小和过滤器计数，不记录原始查询文本 | 不重复记录操作日志；敏感数据访问由独立安全日志记录 |
| Security log | 无；普通操作日志访问不属于安全事件 | `security_log_accessed`，成功返回前写入异步队列；无法入队则拒绝返回，记录操作和结果数 |
| Cache | 不缓存；日志查询敏感且实时性要求高 | 不缓存 |
| Distributed lock | 无；只读操作不争用业务不变量 | 无 |
| Optimistic lock | 不适用；只读 | 不适用 |
| Audit | 查询日志通过共享 Recorder 获取当前主体；日志表仍由数据库触发器维护审计字段 | 同左 |
| Presentation | RFC 3339/Asia/Shanghai；沿用现有人员展示名和敏感字段隐藏规则 | 同左，`token_id_hash` 永不输出 |
| Tests | 服务层范围、时间窗、租户上下文、参数化 SQL；HTTP 授权注册和请求校验单测 | 同左，另验证访问审计失败时不返回数据 |
| Shared capability | 复用日志查询、分页、响应、错误码、平台授权与审计 Recorder | 同左 |
| Dictionary provider | 日志高基数且敏感，不适合作为字典 | 同左 |

权限矩阵：四个 POST 路由均绑定 JWT、平台资源及 `read`/`list` 动作，数据权限免除理由是服务层固定系统日志范围；不依赖 HTTP 路径作为权限身份。`get` 使用 ID，`page` 支持现有精确/IN、关键字和时间范围过滤，不开放不受控排序。`occurred_at_from` 包含、`occurred_at_to` 不包含，最大 31 天。禁止缺少时间窗的全分区 count。
