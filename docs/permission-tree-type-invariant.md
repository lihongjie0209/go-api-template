# Permission node-type update invariant

`POST /api/v1/permissions/update` and system permission seed reconciliation
must validate the complete proposed tree, including the candidate node type.
A group with children cannot become a permission; a leaf group may become a
permission. The frontend mirrors this rule for presentation, but the database
transaction remains authoritative.

| Concern | Decision |
| --- | --- |
| Contract | Keep the existing update request and global `invalid` error; `id` and expected `version` remain mandatory. No new endpoint or filter. |
| Authentication | JWT with the existing permission-definition update authorization; seed reconciliation is internal-only. |
| Authorization | Existing `permission.definition:update` check applies before mutation; this invariant grants no additional access. |
| Operation log | Existing transactional `permission.update` / `permission.seed` record; failures follow the current failure-recording path. |
| Security log | Existing permission-change security event; no new event type. |
| Cache | Existing write path remains the invalidation owner; no new cached state. |
| Distributed lock | None; the current Serializable transaction and row-version boundary protect concurrent tree changes. |
| Optimistic lock | Preserve `WHERE id=? AND version=?` for update; seed continues its deterministic-ID reconciliation path. |
| Audit | Existing transaction actor context and audit triggers maintain all audit fields. |
| Presentation | Tree shape and node labels are unchanged; frontend hides invalid node-type choices. |
| Tests | Unit test proposed group→permission with/without children; Testcontainers lifecycle covers rejection, unchanged version, and valid leaf conversion on PostgreSQL/MySQL in CI. |
| Shared capability | Reuse the existing tree builder and parent-type validator; no new SDK abstraction. |
| Dictionary provider | Permission definitions are sensitive authorization configuration, not a public dictionary. |
