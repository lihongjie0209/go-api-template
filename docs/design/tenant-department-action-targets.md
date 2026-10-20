# Tenant department update/delete-only targets

The department tree requires `tenant.department:list`. An operator granted only
`tenant.department:update` or `tenant.department:delete` must still be able to
act on a known department UUID without acquiring tree/read permission.

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/tenant-departments/update-target` returns `{id,parent_id,parent_name,code,name,sort_order,version}` and `/delete-target` returns `{id,code,name,version}` for `{id:"uuid"}`. Invalid UUID is invalid request; absent, deleted, cross-tenant, or data-scope-denied rows are not found. Single targets do not paginate, sort, or search. |
| Authentication | JWT with an active tenant context. |
| Authorization | Each route binds its own `tenant.department` action with required data scope. The existing `Get` path applies the action-specific SQL predicate, tenant ID, and logical-delete filter. Update/delete repeat scope and ownership checks. The target is not a read/list grant. |
| Operation log | None for target reads. Existing mutation commands record outcomes. |
| Security log | None for target reads; existing department changes preserve their current security-event behavior. |
| Cache | None; confirmation needs the latest version. |
| Distributed lock | None for reads. Existing writes use the tenant tree lock, transaction, and constraints. |
| Optimistic lock | Commands submit the target version. A conflict discards the stale target and requires re-query, never an automatic write retry. |
| Audit | Read-only targets; writes retain transaction-local actor and audited trigger behavior. |
| Presentation | Update returns `parent_name` when the parent is in scope. If a parent is not visible, the UI labels it as unavailable without rendering the opaque ID as display text. Parent UUID may be entered explicitly for moves; the server validates parent existence, tenant, cycles, and data transition. |
| Tests | Unit tests cover projection, invalid ID, missing tenant/data scope, response fields, action-only entry, versioned update/delete, conflict, principal change, and post-commit tree synchronization. A Testcontainers lifecycle case is reserved for the later CI integration run. |
| Shared capability | Reuse scoped `Get`, Resource/Action descriptors, shared response/error envelope, `GoReadByIDPanel`, known-target lifecycle, versioned editor, and tree page. No new SDK implementation. |

Departments are transactional and unsuitable as static data dictionaries; the
existing in-process dynamic provider remains the owned dictionary contract.
