# 手动触发定时任务：异步派发契约

## 现状与目标

`POST /api/v1/scheduled-jobs/trigger` 当前在 HTTP 请求中同步执行任务。HTTP 请求默认上限为 10 秒、写超时为 15 秒，而任务允许执行 3600 秒；因此长任务会被请求取消，调用方也无法可靠判断是否已执行。接口应只提交一个持久的执行请求，立即返回执行请求 ID。执行结果由运行记录接口查询。

## 接口设计决策

| 关注点 | 决策及原因 |
| --- | --- |
| 契约 | 保留 `POST /api/v1/scheduled-jobs/trigger` 和 `{id}` 请求；成功返回 `{run_id,status:"queued"}`。这是“已接受”，不是“执行成功”。输入缺失或任务不存在沿用全局参数/不存在错误码。恢复中的 worker 对持久化任务快照重新校验名称、handler、时限、锁租约及 JSON payload；非法快照不执行 handler，写入通用错误终态。 |
| 认证 | JWT；沿用已注册的 `scheduled-job:execute` 资源动作和平台作用域，不放宽为匿名或 PSK。 |
| 授权 | 先授权再查询任务和提交请求；处理器只执行已在本服务注册的 handler，不接收客户端指定的执行代码或 payload。 |
| 操作日志 | 提交事务记录 `platform.scheduled-job.execute`，表示执行请求已接受；完成事务另记录 `platform.scheduled-job.run.completed`，表示 cron/手动任务的真实终态，关联 run ID 与 Request ID。运行记录和完成日志必须同事务提交。手动任务日志不可入队时回滚终态并待租约到期重试；cron 无可重试请求，记录故障并暴露执行错误。拒绝提交由入口错误记录。 |
| 安全日志 | 不记录；此操作不改变身份、会话、成员或授权状态。 |
| 缓存 | 不缓存提交结果或运行状态；数据库是权威来源。 |
| 分布式锁 | 沿用 `cron:<job-code>` 的最小冲突键及续租；同一任务的 cron/手动执行互斥，租约丢失取消 handler。 |
| 乐观锁 | 提交是新增记录，不需要期望版本；运行状态转移使用版本/所有权条件更新，重复消息不能重复执行。 |
| 审计 | 请求记录和运行记录包含完整审计字段，由事务 actor 与数据库触发器维护；后台处理使用显式系统 actor。运行表的 CHECK 约束限制超时、锁租约和 payload 字节数；worker 在执行前重验快照，拒绝非法 JSON 或异常业务字段。 |
| 展示 | 返回 run ID 供前端跳转/轮询。所有时间保持带时区的 RFC 3339；任务名称随运行记录快照，执行中的状态和失败摘要清晰区分。 |
| 可观测性 | 提交事务同时保存 `request_id`、`trace_id` 和有界的 W3C `traceparent`/`tracestate`，后台认领后恢复 Trace Context 并创建执行 span。只持久化 Trace Context，不复制 baggage、凭据或请求头；旧运行记录没有 Trace Context 时启动独立 span。终态写库失败记录到该 span 和带 Request ID/Trace ID 的日志，并将本次运行指标标为错误；指标标签仅使用注册的 handler 名称。 |
| 测试 | 单测覆盖请求超时不取消已接受任务、重复消息、并发争抢、handler 失败、租约丢失、关闭恢复、异常持久化快照拒绝执行及跨重启 Trace Context 恢复；带 `integration` 标签的数据库/消息队列测试由 CI 跑，并核对持久化的 Trace Context。 |
| 共享能力 | 复用 SDK 分布式锁、现有事务 outbox、事件总线及后台生命周期；不新增临时 goroutine 或 Redis 队列。 |
| 数据字典 | 运行记录含链路及错误数据，不适合字典；状态枚举由接口文档定义。 |

## 持久化与状态机

提交请求必须在同一个数据库事务中写入待执行记录和 outbox 事件，不能在提交后直接发布消息。消息消费者在一个短事务中设置 `dispatched_at` 后确认消息；未被消息消费者确认的记录不得由 worker 认领。实际长任务由服务自身四个受生命周期管理的 worker 执行，不能占用消息消费者 25 秒的默认处理窗口。worker 在数据库中原子认领待处理记录，设置每次认领独立的随机 owner token 和有界租约并周期续约；服务重启后可重新认领已过期租约。状态限定为 `queued -> running -> success|error|skipped`，重复投递或过期 worker 无权覆盖最终结果。`skipped` 用于同一任务的分布式锁已被占用。任务执行超时以任务定义为准，且不继承 HTTP 请求 deadline。执行结束或超时后记录耗时、关联 Request ID/Trace ID 和不含敏感信息的错误摘要。执行器若有非幂等副作用，必须使用 run ID 作为业务幂等键；跨进程崩溃可能导致一次任务被重试。

手动运行记录保存提交时已校验的任务 payload 快照，上限为 1 MiB，用于故障恢复后执行同一请求；该字段不向查询接口返回，也不进入操作日志。MySQL 的 `scheduled_job_runs.payload` 使用 `MEDIUMTEXT`，避免合法边界请求超过 `TEXT` 的 65,535 字节容量。提交时另保存最大 55 字节的 `traceparent` 和 512 字节的 `tracestate`；worker 独立于 HTTP/消息消费者运行，必须从这两个持久字段恢复父 Trace，不能只依赖展示用的 `trace_id`。数据库字段长度与内存恢复入口均限制大小，并由 OpenTelemetry 校验格式。

部署运行表约束迁移前，应先检查历史行是否有超出 1–3600 秒任务超时、1–86400 秒锁租约或 1 MiB payload 的快照；不合规记录需按运行状态审查并修复，迁移不能静默截断。worker 即使遇到受污染快照，也不将其传给 handler 或分布式锁；它尝试记录通用错误终态，并以固定低基数名称写完成日志和指标。若数据库约束或日志 outbox 导致终态无法提交，则保留租约恢复路径并记录持久化故障，等待运维修复，不能谎报执行成功。

PostgreSQL、Kingbase 和 MySQL 上线迁移 `000047` 前可先运行以下只读预检；结果必须为 0，再启动服务自动迁移：

```sql
SELECT count(*) AS invalid_run_count
FROM scheduled_job_runs
WHERE timeout_seconds NOT BETWEEN 1 AND 3600
   OR lock_ttl_seconds NOT BETWEEN 1 AND 86400
   OR octet_length(payload) > 1048576;
```

前端只在提交成功时提示“任务已提交”并显示返回的 run ID，不能提示“执行完成”。运行结果由 `/scheduled-job-runs/get` 或 `/page` 展示，默认按提交时间排序，避免不同数据库对空开始时间的排序差异。全链路失败处理应覆盖 outbox 派发重试/死信、消费者重复投递、worker 崩溃、数据库不可用、锁不可用及停机取消；超过重试上限的请求可通过现有 `outboxctl` 死信重放入口修复，不能静默丢失。

同步调度路径即使遇到已持久化任务的 handler 未注册，也必须写入失败运行记录与完成操作日志事件；不能只返回内存错误而丢失历史。日志中只保留通用失败摘要，不持久化原始 handler 错误。

## 执行完成日志补充决策

| 关注点 | 决策及原因 |
| --- | --- |
| 契约 | 不新增 HTTP 接口；任务运行记录继续是状态权威来源。平台系统操作日志可按同一 Request ID 查询 `platform.scheduled-job.run.completed`。 |
| 认证与授权 | 运行写入是内部系统主体；查看仍由 `scheduled-job:read/list` 和 `platform.operation-log:read/list` 分别授权。 |
| 操作/安全日志 | 成功、失败、锁竞争跳过均写一条完成操作日志；不写安全日志，因为任务终态不是身份或权限事件。只保存安全的任务标识、run ID、触发来源与状态，不保存任务 payload 或原始错误。 |
| 缓存/分布式锁 | 不缓存日志；执行锁仍为 `cron:<job-code>`，不扩大锁粒度。 |
| 乐观锁与审计 | cron 创建运行记录；手动运行以 owner token 和未过期租约更新。日志 outbox 写入与运行状态写入同一事务，数据库触发器维护审计字段。 |
| 展示 | `resource_id` 是运行 ID，`resource_name` 是任务名称；时间仍为 RFC 3339/Asia/Shanghai，Request ID 与执行记录一致。 |
| 测试 | 单测覆盖 cron 事务成功/回滚，手动完成成功/回滚、丢失租约不写日志及上下文关联；PostgreSQL/MySQL Testcontainers 集成测试覆盖手动触发时运行记录与 outbox 同事务提交、outbox 不可用时回滚运行记录，以及同步执行时终态与完成日志事件的同事务提交/回滚。租约 worker 的终态与日志跨表原子性仍需补充集成验证。 |
| 共享能力与字典 | 复用 `operationlog.TransactionalRecorder`；运行日志高基数且敏感，不作为数据字典提供者。 |
