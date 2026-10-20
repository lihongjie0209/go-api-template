# Tenant role grant target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/tenant-roles/permissions/target` accepts `{ "id": "role UUID" }` and returns `{id,code,name,status,version,permissions}` in the shared envelope. Each linked permission includes mandatory `assignable: boolean`; false means it must be removed before save. It is a bounded, non-paginated edit snapshot: the existing setter limits assignments to 1000. Invalid/missing/foreign or out-of-scope roles use global errors. |
| Authentication | Tenant-scoped JWT; no public or PSK path. |
| Authorization | `tenant.role:grant` with required row data permission. Role lookup includes authenticated tenant and compiled row scope; linked permissions are read only after that lookup. The setter repeats authoritative checks. |
| Operation log | No log for target read. The existing `tenant.role.permissions.set` mutation records the change. |
| Security log | No security event for target read. The existing privilege-change mutation logs the security event. |
| Cache | None; linked permissions and role version must be fresh before editing. |
| Distributed lock | None for target read. The setter already locks the tenant+role key and checks the database version. |
| Optimistic lock | Return current role version; the setter requires `role_id` and `version` and rejects stale writes. |
| Audit | Read only; the existing setter uses actor-aware transaction/audit maintenance. |
| Presentation | Return code/name/status and linked permission key/name/resource/action/status/assignable; disabled, revoked, or above-ceiling old links remain visible but cannot be resubmitted. |
| Tests | Unit tests for bounded tenant/data scope, missing role, linked permission snapshot, malformed input and HTTP response. Frontend tests for grant-only entry, no-read transport, stale and principal changes. Database integration remains for the later CI run. |
| Shared capability | Reuse the role service, shared route descriptor, response/error handling and frontend selection drawer. No new general infrastructure. |
| Data dictionary | No provider; role-permission grants are sensitive transactional authorization data. |

| Operation | Auth | Resource/action | Object check | Data filter |
| --- | --- | --- | --- | --- |
| `/tenant-roles/permissions/target` | JWT | `tenant.role:grant` | Existing role in current tenant | Required role-row data scope |
