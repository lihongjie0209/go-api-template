# 租户应用授权：仅撤销权限入口

仅持有 `tenant.application-grant:revoke` 的平台操作员不能依赖授权列表或读取权限获取当前版本。操作员输入已知租户 ID 和授权 ID，由同一动作保护的窄目标接口确认身份和版本，再调用既有乐观锁撤销命令。

| 关注点 | 决策与依据 |
| --- | --- |
| 契约 | `POST /api/v1/platform/tenant-applications/revoke-target` 接收 `{tenant_id,id}`；只返回授权、租户、应用的 ID/名称、状态和版本。空 ID、超长 ID 返回全局参数错误；不存在返回全局未找到；已撤销返回全局冲突。无分页、排序或筛选。 |
| 认证 | JWT；不加入免认证或 PSK 通配符。 |
| 授权 | 注册为 `tenant.application-grant:revoke`、平台范围、无数据权限；服务层要求平台主体，SQL 同时约束 `tenant_id`、授权 ID 和逻辑删除。命令仍独立重复鉴权。 |
| 操作日志 | 目标查询不记录；既有撤销命令记录 `tenant.application.revoke` 的成功和失败。 |
| 安全日志 | 目标查询不记录；撤销的既有安全事件链路不变。 |
| 缓存 | 不缓存。版本和状态必须在确认前读取权威数据库。 |
| 分布式锁 | 不加锁。既有撤销命令的数据库事务和版本谓词负责并发正确性。 |
| 乐观锁 | 目标查询不写入；撤销命令必须提交目标返回的 `version`，过期时要求重新查询。 |
| 审计 | 目标查询不修改审计字段；撤销命令继续由数据库事务和审计触发器维护。 |
| 展示 | 返回 `tenant_name`、`application_name` 和 `application_code`，避免只显示 ID；不返回应用 URL、图标、审计人员或其他无关字段。 |
| 测试 | 单测覆盖最小响应、无效/缺失/已撤销目标、租户范围、平台主体要求和撤销提交版本；集成测试沿用服务内 Testcontainers CI，不在本地触发。 |
| 共享能力 | 复用已有 Resource/Action 拦截、错误响应、事务、`GoReadByIDPanel` 和授权按钮；授权是事务数据，不作为数据字典提供者。 |

权限矩阵新增行：`revoke-target` 与 `revoke` 均为 JWT + `tenant.application-grant:revoke`、平台范围；前者只读已知目标，后者改变授权状态。仅有撤销权限不推导出列表或普通详情读取权限。
