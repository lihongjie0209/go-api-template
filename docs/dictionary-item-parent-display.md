# Dictionary item parent display

The authenticated dictionary item `create`, `get`, `page`, and `update` responses
include `parent_name` alongside `parent_id`. Root items return an empty
`parent_name`; the UI labels them as root nodes. A missing or cross-dictionary
parent is not used as display data. The public dictionary `value` is a business
value and must never stand in for a parent item UUID.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Keep the existing POST JSON inputs, envelope and global errors. Add only `parent_name` to item responses; `get` uses `id`, `page` keeps bounded pagination and existing keyword/disabled/parent filters. No new sort or filter identifier is accepted. |
| Authentication | Existing JWT route bindings remain; no public management route is added. |
| Authorization | Existing `dictionary.item` create/read/list/update actions are checked before service access. Definitions and items are platform-scoped, not tenant-owned. |
| Operation log | Create and update retain their existing transactional operation events and safe summaries; get/page emit none because they are ordinary reads. |
| Security log | None; these endpoints do not change authentication, credentials or privilege state. |
| Cache | None for management reads; `parent_name` is resolved from the authoritative dictionary table in one bounded batch. Public dictionary query retains its separate existing cache contract. |
| Distributed lock | None; this presentation read changes no contention invariant. |
| Optimistic lock | Update retains its required `id` and `version` predicate; create/get/page do not update an existing row. |
| Audit | Existing audited table trigger and transaction actor context remain unchanged. Presentation does not write audit fields. |
| Presentation | `parent_name` is resolved only from a nondeleted parent in the same dictionary; enum/root items have no parent. The frontend shows the name rather than an opaque ID and distinguishes root from an unavailable parent. Existing timestamps remain RFC 3339 with offset. |
| Tests | Unit SQL mock covers a same-dictionary parent, cross-dictionary rejection and root without a query. Existing Testcontainers lifecycle now asserts create/get/page display; CI runs it under the `integration` tag, not local development. |
| Shared capability | Reuses existing dictionary presentation and resource table/detail components. No new cross-service infrastructure abstraction is needed. |
| Dictionary provider | No new provider: item management is an authenticated administrative resource; public dictionary querying already has the bounded provider contract. |
