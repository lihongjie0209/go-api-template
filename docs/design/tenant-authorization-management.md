# Tenant authorization management interfaces

## `POST /api/v1/platform/tenant-authorization/permissions/get`

| Concern | Decision |
| --- | --- |
| Contract | Accept `{tenant_id}` and return the tenant permission ceiling as `[]PermissionView`, ordered by permission key. |
| Authentication | JWT; platform principal only. |
| Authorization | `tenant:grant`, platform scope. Reading the grant configuration is part of managing it and is not exposed through the broader `tenant:read` action. |
| Operation log | None; this is a query. |
| Security log | None; no authorization state changes. |
| Cache | None; the management read must reflect the authoritative database immediately after a grant change. |
| Distributed lock | None; read-only. |
| Optimistic lock | Not applicable; the paired set command requires the tenant version. |
| Audit | No writes. Soft-deleted grants and permissions are excluded. |
| Presentation | Return permission ID, stable key, name, resource and action; never return opaque IDs alone. |
| Tests | Platform allow, tenant-principal deny, validation, missing tenant, empty result, ordering and database failure. |
| Shared capability | Reuses the global response, principal, PBAC descriptor and permission presentation model. |
| Dictionary provider | Not exposed: permission ceilings are sensitive authorization configuration, not bounded reference data for general selection. |

## `POST /api/v1/platform/tenant-authorization/administrators/page`

| Concern | Decision |
| --- | --- |
| Contract | Standard page request plus `tenant_id`, membership/user/status filters and joined-time range; returns tenant members with `is_administrator`. Keyword searches username and display name. IN lists are capped at 200 and time ranges are `[from,to)`. |
| Authentication | JWT; platform principal only. |
| Authorization | `tenant:assign-administrator`, platform scope. |
| Operation log | None; this is a query. The existing set command records the mutation. |
| Security log | None; this read does not change privileges. The existing set command records privilege changes. |
| Cache | None; administrator state is security-sensitive and must be current. |
| Distributed lock | None; read-only. The set command retains its tenant-scoped lock. |
| Optimistic lock | Not applicable to this query. Membership versions are returned for display but administrator mutation remains the existing guarded command. |
| Audit | No writes. Membership, tenant and administrator soft-delete predicates are applied in SQL. |
| Presentation | Include username and display name beside user/membership IDs; timestamps use RFC3339 with the platform timezone. |
| Tests | Platform scope, input validation, tenant isolation, identical count/item predicates, stable sorting, inactive-member visibility, empty page and cancellation/database failures. |
| Shared capability | Reuses pagination, principal, response and PBAC registration components. |
| Dictionary provider | Not exposed: tenant membership and administrator assignment are sensitive transactional data. |

## `POST /api/v1/tenant-authorization/administrators/page`

| Concern | Decision |
| --- | --- |
| Contract | Standard page request with membership/user/status filters and a joined-time `[from,to)` range. The tenant ID is never accepted from the request; it is taken from the authenticated tenant context. Returns members with `is_administrator`, ordered by joined time and membership ID. |
| Authentication | JWT user principal with a non-empty tenant and membership context. |
| Authorization | `tenant.authorization:assign-administrator`, tenant scope. The service additionally verifies that the caller is an active administrator of the same tenant before querying candidates. |
| Operation log | None; this is a query. The set command records changes. |
| Security log | None for reads; the existing administrator mutation records privilege changes. |
| Cache | None; administrator assignment is security-sensitive and must reflect the authoritative database. |
| Distributed lock | None; read-only. The set command retains the smallest tenant-scoped lock. |
| Optimistic lock | Not applicable to the query. Membership versions are returned; administrator mutation uses its existing serializable transaction and invariant that at least one active administrator remains. |
| Audit | No writes. The query filters soft-deleted tenants, memberships and assignments. |
| Presentation | Return username and display name with IDs; render joined timestamps as RFC3339 in `Asia/Shanghai`. |
| Tests | Missing/platform/wrong-tenant principals fail before candidate SQL; non-administrator deny; exact tenant predicate on count and items; validation, cancellation and database failures. HTTP route/Swagger/authorization coverage remain CI invariants. |
| Shared capability | Reuses pagination, principal, PBAC descriptors, response envelope and the existing candidate query implementation. |
| Dictionary provider | Not exposed because membership and privilege assignment are sensitive transactional data. |

## `POST /api/v1/tenant-authorization/effective-permissions`

| Concern | Decision |
| --- | --- |
| Contract | Accept optional `membership_id`; empty means the current membership. Return ordered `PermissionView` objects containing ID, stable permission key, name, resource and action rather than opaque IDs. |
| Authentication | JWT tenant user principal. |
| Authorization | `tenant.authorization:read`. Reading another membership additionally requires the caller to be a current active tenant administrator. |
| Operation log | None; query only. |
| Security log | None; no privilege state changes. |
| Cache | None initially; effective permissions are security-sensitive and role/grant changes must be visible immediately. |
| Distributed lock | None; read-only. |
| Optimistic lock | Not applicable. |
| Audit | No writes; membership, tenant, role and permission soft-delete/status predicates apply. |
| Presentation | Return names and canonical keys beside permission IDs so the frontend never renders opaque identifiers. |
| Tests | Current-member and administrator-other-member reads, non-admin deny, inactive membership, empty permission set, database failure and stable ordering. |
| Shared capability | Reuses the same effective-ID calculation and `PermissionView` projection used by assignable permissions. |
| Dictionary provider | Not exposed; effective authorization state is sensitive and principal-specific. |
