# Tenant authorization link reconciliation

The existing tenant permission-ceiling, role-permission, and member-role POST
commands replace bounded association sets. This change keeps their requests,
responses, and registered Resource/Action descriptors.

| Concern | Decision and rationale |
| --- | --- |
| Contract | At most 1,000 distinct requested IDs; an existing active set over 1,000 fails closed instead of issuing an unbounded mutation. Existing conflict and invalid-input codes remain. |
| Authentication | Existing JWT tenant principals and platform-admin routes remain. |
| Authorization | Existing permission ceilings, delegation, tenant ID predicates, and object data scopes remain authoritative. |
| Operation log | Existing set command and bounded IDs/counts are recorded atomically. |
| Security log | Existing privilege-change event remains transactional and fail closed. |
| Cache | Existing policy revision invalidation remains owned by the grant mutation after commit. |
| Distributed lock | Existing tenant authorization locks stay at their reviewed scope; SQL CAS and uniqueness remain final concurrency guards. |
| Optimistic lock | Read at most 1,001 active links, preserve requested active rows, delete only removed rows by `id,version`, and restore one deterministically chosen deleted row by `id,version`. Administrator revocation locks its single active link and also deletes by `id,version`. Check exactly one affected row. |
| Audit | SQL actor context and triggers maintain link timestamps, actor IDs, soft deletion, and versions. Unchanged links retain their audit state. |
| Presentation | Existing role/permission display names and +08 timestamps remain. |
| Tests | Unit tests cover unchanged rows, empty replacement, stale delete/restore, oversized active sets, and administrator link conflicts. The service-contained PostgreSQL/MySQL integration suite covers grant and role lifecycle. |
| Shared capability | Reuse the existing transactor, authorization service, audit recorders, and common error codes. |

These association links are transactional and are not public data-dictionary
providers. Table and value-column identifiers are selected by code-owned call
sites; caller IDs remain bound SQL parameters.
