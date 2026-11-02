# Tenant authorization link reconciliation

The existing tenant permission-ceiling, role-permission, and member-role POST
commands replace bounded association sets. This change keeps their requests,
responses, and registered Resource/Action descriptors.

| Concern | Decision and rationale |
| --- | --- |
| Contract | At most 1,000 distinct requested IDs; an existing active set over 1,000 fails closed instead of issuing an unbounded mutation. Existing conflict and invalid-input codes remain. Database serialization/deadlock aborts map to the global conflict response after rollback, without exposing driver details. |
| Authentication | Existing JWT tenant principals and platform-admin routes remain. |
| Authorization | Existing permission ceilings, delegation, tenant ID predicates, and object data scopes remain authoritative. |
| Operation log | Existing set command and bounded IDs/counts are recorded atomically. |
| Security log | Existing privilege-change event remains transactional and fail closed. |
| Cache | Existing policy revision invalidation remains owned by the grant mutation after commit. |
| Distributed lock | Existing tenant authorization locks stay at their reviewed scope; SQL CAS and uniqueness remain final concurrency guards. Member-role assignment also locks selected active role rows in ID order. Role deletion updates the role row before clearing member links, so assignment cannot commit a link to a deleted role. |
| Optimistic lock | Read at most 1,001 active links, preserve requested active rows, delete only removed rows by `id,version`, and restore one deterministically chosen deleted row by `id,version`. Administrator revocation locks its single active link and also deletes by `id,version`. Role deletion increments the versions of affected membership rows before clearing their role links; a stale member-role editor request then conflicts. Check exactly one affected row for single-link commands. |
| Audit | SQL actor context and triggers maintain link timestamps, actor IDs, soft deletion, and versions. Unchanged links retain their audit state. |
| Presentation | Existing role/permission display names and +08 timestamps remain. |
| Tests | Unit tests cover unchanged rows, empty replacement, stale delete/restore, oversized active sets, administrator link conflicts, missing active role rejection, role-deletion SQL order, and rollback on affected-member update failure. The service-contained PostgreSQL/MySQL integration suite now checks successful administrator revocation, persisted audit versions, unchanged membership version after a stale role-delete attempt, and stale member-role rejection after successful role deletion; CI execution is pending. |
| Shared capability | Reuse the existing transactor, authorization service, audit recorders, and common error codes. |

These association links are transactional and are not public data-dictionary
providers. Table and value-column identifiers are selected by code-owned call
sites; caller IDs remain bound SQL parameters.

Migration `000052` adds a tenant/role/member lookup index for role deletion's
set-based membership version update and association cleanup. No member-ID set
is materialized in Go.
