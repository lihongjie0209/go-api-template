# Tenant role update/delete-only targets

An operator with `tenant.role:update` or `tenant.role:delete` but no role
read/list grant needs an action-scoped way to obtain a current version. The
known-ID workflow never treats either action as a general read grant.

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/tenant-roles/update-target` and `/tenant-roles/delete-target` take `{ "id": "uuid" }`. Update returns `{id,code,name,description,status,version}`; delete returns `{id,code,name,status,version}`. Invalid UUID is invalid request; absent, deleted, other-tenant, or out-of-data-scope role is not found. Single targets have no pagination, filtering, or sorting. |
| Authentication | JWT with an active tenant context. |
| Authorization | Each route binds its matching `tenant.role` action and requires data permission. The service compiles the action-scoped role predicate, and the repository query includes tenant ID and logical-delete filtering. Commands repeat the same checks; possession of a target does not authorize a later write. |
| Operation log | No log for target reads. Existing update/delete commands record mutation outcomes. |
| Security log | No log for target reads. Existing role mutation logging remains unchanged. |
| Cache | None; each confirmation must use the current authoritative version. |
| Distributed lock | None for reads. Existing commands use a per-tenant, per-role lock and database transaction. |
| Optimistic lock | Update/delete submit `id` and target `version`; a failed version predicate returns the global stale-version code, clears the target, and requires a new lookup, never an automatic retry. Unique-key or assignability conflicts retain the separate business-conflict code. |
| Audit | Targets are read-only. Commands use the existing transaction actor and audited role/link tables. |
| Presentation | Both targets include code/name; update includes editable description/status. No tenant ID, permission grants, audit identities, or timestamps are returned. |
| Tests | Service/handler tests cover narrow projection, bad ID, tenant/data-scope denial and absence; frontend API/component/page tests cover action-only entry, versioned writes, stale target invalidation, principal switch, conflict and post-commit synchronization. Later CI integration verifies live persistence and authorization. |
| Shared capability | Reuse the existing scoped role lookup, authorization descriptor, response envelope, `GoReadByIDPanel`, `useKnownActionTarget`, authorized button, and versioned editor. No new SDK abstraction. |

Roles are transactional and security-sensitive, not data-dictionary providers.
