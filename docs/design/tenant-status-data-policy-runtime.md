# Tenant status and data-policy runtime consistency

Tenant-scoped published data policies are usable only while their owning
tenant is active. Disabling a tenant removes its policies from the runtime
snapshot; reactivating it restores the still-published policies. Global
policies are independent of tenant status.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Existing tenant update and admin update contracts retain `id`, `version`, status, and the shared response/error envelope. No new route or filter is introduced. |
| Authentication | Tenant update remains JWT; admin update remains platform-authorized. The policy loader is internal-only. |
| Authorization | Existing resource/action checks and tenant scope stay authoritative. Runtime loading also checks `tenants.status='active'` and `deleted_at IS NULL`, so a stale caller cannot use a disabled tenant's policy snapshot. |
| Operation log | Existing transactional tenant-update event remains the record; policy snapshot refresh is derived state, not another business operation. |
| Security log | Existing tenant status-change security event remains the record; no duplicate event is emitted by refresh. |
| Cache | Policy snapshots are derived. After tenant update commits, publish a refresh hint and refresh the local snapshot; the database revision poll includes referenced tenant rows to recover lost hints. |
| Distributed lock | None; the tenant row update uses expected version, and the runtime replaces complete validated snapshots atomically. |
| Optimistic lock | Existing tenant update requires `WHERE id=? AND version=?`; a zero-row update remains a stale-version error. |
| Audit | Existing transactor, actor context, and database audit triggers maintain tenant audit fields. |
| Presentation | The API returns existing RFC 3339 timestamps and tenant display fields; no new presentation field. |
| Tests | Unit tests cover active-tenant SQL, revision tracking, and refresh after both update paths. Testcontainers cover disable/reactivate snapshots and revision changes on PostgreSQL/MySQL. |
| Shared capability | Reuse `policysync`, the shared transactor, and existing runtime loader; no parallel cache or transport. |

Tenant status is transactional/sensitive and is not exposed as a data-dictionary
provider. Normal authorization still rejects disabled tenants before business
handlers; the snapshot filter is an independent fail-closed boundary.
