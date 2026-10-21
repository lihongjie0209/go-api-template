# Tenant-scoped data policy creation

`POST /api/v1/data-permission/policies/create` must not persist a tenant policy
for a missing, deleted, or disabled tenant. The runtime loader deliberately
excludes missing/deleted tenants, so accepting such a write produces a policy
that appears published but cannot be applied. This decision applies to the
tenant-scoped variant; global policies do not look up a tenant.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Keep the existing policy document and response. A non-active tenant returns the shared invalid-policy error; no new route or pagination rule. |
| Authentication | Existing JWT and service-account transport rules remain unchanged; this is an additional service/repository boundary. |
| Authorization | Existing resource/action and tenant-scope checks run first. Inside the write transaction, lock the exact active tenant row, preventing a concurrent disable/delete before commit. |
| Operation log | Existing transactional policy-create event records successful writes; existing failure recorder handles rejected attempts without persisting a policy. |
| Security log | No new security event: policy creation already uses the current privilege-change logging path. |
| Cache | No new cache. The authoritative tenant status is read from the database to avoid stale acceptance. |
| Distributed lock | None; the tenant row lock and transaction serialize the relevant state transition. |
| Optimistic lock | Not applicable to create; subsequent version/status mutations retain their existing expected-version checks. |
| Audit | Existing transaction actor and audit trigger maintain all policy/action/version audit fields; the failed check rolls back. |
| Presentation | No new timestamp or related-ID fields; the existing policy response contract is unchanged. |
| Tests | Unit tests cover active, missing/inactive, and database failure; PostgreSQL/MySQL Testcontainers cover real active and missing tenant behavior. |
| Shared capability | Reuse the shared transactor and principal context; no new SDK abstraction is needed for this module-specific tenant existence check. |

The policy document is configuration, not a bounded read-only dictionary, so
this module does not register it as a data-dictionary provider.
