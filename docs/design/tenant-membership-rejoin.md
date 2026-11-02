# Rejoining a removed tenant member

`tenant.member.add` restores an existing logically deleted membership for the
same tenant and user. This gives PostgreSQL, MySQL and Kingbase the same
behavior despite different historical unique-index definitions. The restored
row keeps its ID and original `created_at`/`created_by`, receives new join and
update timestamps, clears delete fields, and advances its optimistic version.
Previously removed role, department and administrator links remain deleted.
PostgreSQL migration `000051` adds a partial index for locating deleted
membership history; MySQL and Kingbase use their existing tenant/user unique
indexes for that lookup.

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/tenant-members/add` still accepts a username and returns a member; a rejoin returns the restored ID and incremented version. An active duplicate or stale restore returns global conflict. |
| Authentication | Existing JWT and `tenant.member:add` endpoint descriptor remain mandatory. |
| Authorization | Resolve the current active identity first. Authorize the server-built member candidate using the restored ID and persisted creator when restoring; insertions use the new ID and current actor. Every lookup and write binds the actor's tenant. |
| Operation log | Existing transactional `tenant.member.add` event uses the actual restored or inserted membership ID. Failed attempts use the existing safe failure event. |
| Security log | Existing transactional `membership_added` event records the target user and tenant. |
| Cache | No membership response cache; identity resolution remains authoritative for eligibility. |
| Distributed lock | Reuse the narrow `tenant:<tenant ID>:membership:<user ID>` lock around lookup and write; SQL version/unique predicates remain authoritative if the lock is absent or lost. |
| Optimistic lock | A restore updates only `WHERE tenant_id,id,user_id,version,deleted_at IS NOT NULL`; zero rows is a conflict. Ordinary member update/removal retains submitted versions. |
| Audit | Original creation fields are retained. The actor, join time, update time, delete reset and version are maintained in the transaction under database audit triggers. |
| Presentation | Response keeps user and actor display names and +08 times. |
| Tests | Unit regressions cover restore and concurrent/stale restore. The existing PostgreSQL/MySQL Testcontainers member lifecycle will exercise remove then rejoin; run only in CI. |
| Shared capability | Reuses the platform lock, transaction, data-scope, operation-log and security-log components. Membership is transactional and not a public dictionary provider. |
