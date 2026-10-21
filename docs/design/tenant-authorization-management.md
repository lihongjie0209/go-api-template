# Tenant authorization management interfaces

## Administrator assignment optimistic version (`POST .../administrators/set`)

| Concern | Decision |
| --- | --- |
| Contract | Both platform and tenant POST routes require `{tenant_id,membership_id,enabled,version}` with positive membership version. Return the shared empty success envelope; invalid input, forbidden scope, missing membership and stale version use global errors. |
| Authentication | JWT on both routes; no anonymous or PSK access. |
| Authorization | Platform route uses `tenant:assign-administrator`; tenant route uses `tenant.authorization:assign-administrator` plus current administrator and same-tenant checks. Membership must be active and tenant-scoped. A failed administrator lookup fails closed and preserves its dependency error rather than masquerading as an ordinary deny. |
| Operation log | Record `tenant.administrator.set` with target ID, display name, tenant and enabled state; do not log credentials. |
| Security log | Record the existing tenant-authorization privilege-change event transactionally. |
| Cache | Do not cache the administrator list; mutation retains the shared authorization invalidation path. |
| Distributed lock | Retain the existing per-tenant administrator lock and serializable transaction for the last-administrator invariant; propagate its lease context. |
| Idempotency | Both platform and tenant administrator mutation routes are opted into the configured HTTP idempotency middleware; the UI supplies a key per submitted command. A key never replaces the membership version predicate. |
| Optimistic lock | Compare and increment `tenant_memberships.version` in the same transaction before changing the administrator link. A stale version returns the shared conflict error; a failed transaction rolls back the increment. |
| Audit | The membership update and administrator link use actor-aware audited transactions; no physical delete. |
| Presentation | Candidate page supplies member name and version; timestamps remain RFC3339 with explicit offset. After mutation the frontend refreshes the authoritative candidate page. |
| Tests | Unit tests cover invalid and stale versions, successful compare-and-swap, rollback, and failed administrator lookup classification; the existing tagged database integration case covers assignment and final-administrator protection. Frontend unit tests assert version forwarding. |
| Shared capability | Reuse the existing principal, transactor, lock lifecycle, error envelope, log recorders and authorization mutation invalidation. |
| Dictionary provider | None: administrator assignment is sensitive transactional state, not reference data. |

## `POST /api/v1/platform/tenant-authorization/permissions/target`

| Concern | Decision |
| --- | --- |
| Contract | Accept `{tenant_id}` (non-empty, at most 128 bytes). Return `{id,name,version}` for one non-deleted tenant; missing tenants use the global not-found code. This supplies the expected version for the existing permission-ceiling set command without requiring tenant-list/read access. |
| Authentication | JWT, platform principal only. |
| Authorization | `tenant:grant`, platform scope; no tenant-owned data scope is bypassed. |
| Operation log | None; this is a bounded read. The paired set command logs its mutation. |
| Security log | None; no privilege changes. The paired set command records the security event. |
| Cache | None; the version must be current before an optimistic write. |
| Distributed lock | None; read-only. The set command retains its tenant-scoped lock. |
| Optimistic lock | Read the current tenant version; the subsequent set command still enforces `WHERE id=? AND version=?`. A concurrent change produces the existing version-conflict error. |
| Audit | No writes; exclude soft-deleted tenants. |
| Presentation | Return the tenant name with its ID; no timestamp conversion is needed. |
| Tests | Platform success, invalid ID, tenant-principal denial, deleted/missing tenant, database error, HTTP binding and response shape, frontend stale-context cancellation. |
| Shared capability | Reuse principal, response, global error mapping and Resource/Action route registration. |
| Dictionary provider | None; tenant authorization configuration is sensitive transactional data. |

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
| Shared capability | Reuses the global response, principal, Resource/Action descriptor and permission presentation model. |
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
| Optimistic lock | Not applicable to this query. Membership versions are returned as the required expected version for administrator mutation. |
| Audit | No writes. Membership, tenant and administrator soft-delete predicates are applied in SQL. |
| Presentation | Include username and display name beside user/membership IDs; timestamps use RFC3339 with the platform timezone. |
| Tests | Platform scope, input validation, tenant isolation, identical count/item predicates, stable sorting, inactive-member visibility, empty page and cancellation/database failures. |
| Shared capability | Reuses pagination, principal, response and Resource/Action registration components. |
| Dictionary provider | Not exposed: tenant membership and administrator assignment are sensitive transactional data. |

## `POST /api/v1/platform/tenant-authorization/administrators/target`

| Concern | Decision |
| --- | --- |
| Contract | Accept `{tenant_id}` (non-empty, at most 128 bytes); return the trusted `{id,name,version}` of one non-deleted tenant. Reject missing or invalid IDs and return the global not-found error for unknown tenants. The UI checks the returned ID before opening the administrator drawer. |
| Authentication | JWT platform principal; no public or PSK access. |
| Authorization | `tenant:assign-administrator`, platform scope. An operator with this action does not need the unrelated tenant-list or permission-ceiling grant. |
| Operation log | None for this bounded read; the administrator set command records mutations. |
| Security log | None for the read; the administrator set command records privilege changes. |
| Cache | None; the name and version come from the authoritative tenant row. |
| Distributed lock | None; read-only. The set command retains its tenant-scoped lock. |
| Optimistic lock | The returned version is informational here; administrator set still compares the membership version. |
| Audit | No writes; the shared target query excludes soft-deleted tenants. |
| Presentation | Return a tenant name alongside its ID, including when the administrator drawer is opened by known ID and the operator lacks tenant-list permission. |
| Tests | Handler response/binding, route descriptor, frontend matching/mismatching target, lookup failure, and principal-switch cancellation. |
| Shared capability | Reuse the existing tenant-target lookup, shared response/error mapping, and Resource/Action route registration. |
| Dictionary provider | None; privileged tenant administration is not a general reference-data source. |

## `POST /api/v1/platform/tenant-applications/target/{read,list,grant}`

| Concern | Decision |
| --- | --- |
| Contract | Each POST accepts `{tenant_id}` (non-empty, at most 128 bytes) and returns the trusted `{id,name,version}` of a non-deleted tenant. Missing/invalid IDs use global validation/not-found codes. No pagination or sorting applies. |
| Authentication | JWT platform principal only. |
| Authorization | Three explicit route descriptors use `tenant.application-grant:read`, `:list`, and `:grant` respectively. The frontend selects an endpoint it is allowed to call; no route broadens another action's grant. |
| Operation log | None for this bounded read; grant/revoke mutations remain logged. |
| Security log | None; the read does not change a security setting. |
| Cache | None; resolve the current tenant name from the authoritative row. |
| Distributed lock | None; read-only. |
| Optimistic lock | No write; the returned version is informational. |
| Audit | No writes; the shared query excludes soft-deleted tenants. |
| Presentation | Show the verified tenant name even when no application grants exist; never substitute the opaque ID for a display name. |
| Tests | Route authorization descriptors, valid/missing target handler cases, capability-based frontend endpoint selection, mismatched response, cancellation, and empty-grant opening. |
| Shared capability | Reuse the existing tenant target query, response/error mapping, and Resource/Action registration. |
| Dictionary provider | None; this is a privileged bounded administrative lookup, not public reference data. |

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
| Optimistic lock | Not applicable to the query. Membership versions are returned for the administrator mutation's compare-and-swap; its serializable transaction also preserves the last-administrator invariant. |
| Audit | No writes. The query filters soft-deleted tenants, memberships and assignments. |
| Presentation | Return username and display name with IDs; render joined timestamps as RFC3339 in `Asia/Shanghai`. |
| Tests | Missing/platform/wrong-tenant principals fail before candidate SQL; non-administrator deny; exact tenant predicate on count and items; validation, cancellation and database failures. HTTP route/Swagger/authorization coverage remain CI invariants. |
| Shared capability | Reuses pagination, principal, Resource/Action descriptors, response envelope and the existing candidate query implementation. |
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
