# Historical deleted-tenant policy cleanup

Before tenant deletion cascaded into data-permission tables, a deleted tenant
could leave published policies, versions and action links marked active. The
one-time migration retires only tenant-scoped policies whose tenant is missing
or logically deleted, in child-to-parent order. It preserves disabled but
non-deleted tenants (which can be reactivated), all global policies and all
active tenants' policies. Rows are soft-deleted through the existing audit
triggers; no policy document is destroyed.

The runtime published-policy loader independently requires a non-deleted
tenant for tenant-scoped policies. This protects startup on an old database and
prevents future manual orphan rows from entering the engine. Normal tenant
deletion still updates policy rows and their revision, triggering the existing
refresh/poll mechanism. The migration's `down` is intentionally a no-op:
restoring obsolete grants would be unsafe and cannot distinguish old orphans
from intentionally deleted policy data.

| Concern | Decision and rationale |
| --- | --- |
| Contract | No HTTP/gRPC operation or response changes; this is migration and runtime filtering only. |
| Authentication | No new endpoint. |
| Authorization | Only `scope='tenant'` rows with no non-deleted tenant qualify. Global and live-tenant policies are preserved. |
| Operation log | No business operation event: the migration is deployment maintenance. |
| Security log | No request security event; the migration actor is `system:migration` in audit fields. |
| Cache | Policy row versions change on cleanup, so database revision polling refreshes snapshots; loader also fails closed for orphans. |
| Distributed lock | No business lock; the migration runner serializes migration execution. |
| Optimistic lock | Not applicable to one-time maintenance; normal policy updates retain version checks. |
| Audit | Existing triggers maintain all audit fields and soft-delete metadata. |
| Presentation | No response model or timezone change. |
| Tests | Unit SQL-boundary test for loader plus PostgreSQL/MySQL Testcontainers migration test: orphan removed, live/global preserved, repeatable up. |
| Shared capability | Reuses migration runner, audit triggers and policy runtime; no new SDK component. |
| Dictionary provider | Policies remain sensitive authorization data, unsuitable as a dictionary. |
