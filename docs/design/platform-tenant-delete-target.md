# Platform tenant delete-only path

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/platform/tenants/delete-target` accepts `{ "id": "..." }` and returns `{id,code,name,status,version}` in the shared envelope. The existing delete command consumes ID and version. No list, filter, or sort semantics apply to one exact ID. |
| Authentication | JWT with a platform context (no active tenant); not public or PSK. |
| Authorization | Both target and command bind `tenant:delete`, platform scope. A tenant-context principal is rejected before querying. The target query uses one parameterized ID and logical-delete predicate. |
| Operation log | Target read is not recorded. Existing platform delete records its mutation and outcome. |
| Security log | No event for confirmation read; existing privilege-sensitive delete flow retains its event. |
| Cache | Never cache a version used for deletion confirmation. |
| Distributed lock | None. The existing transactional delete uses version and database constraints. |
| Optimistic lock | Existing `/platform/tenants/delete` requires the returned version; stale writes use the global version-conflict code. |
| Audit | No target write. Existing delete transaction injects the actor for audited soft deletion. |
| Presentation | Return the tenant name and code, not owner IDs or audit data. |
| Tests | Unit tests for platform-only scope, validation, missing row, and narrow response; frontend tests for action-only entry, changed ID/principal/authorization, stale version, and post-commit cleanup. Add a Testcontainers lifecycle assertion but run it in CI. |
| Shared capability | Reuse the resource/action interceptor, shared response/error envelope, confirmation button, and tenant repository. No data dictionary: deletion is a sensitive transactional action. |

Permission matrix: target and delete are JWT, `tenant:delete`, platform scope, explicit ID ownership via platform authority, with no data-permission predicate. The target query is a single exact ID, so pagination, keyword, ranges, and tree behavior are inapplicable.
