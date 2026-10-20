# Department member assignment interfaces

## `POST /api/v1/tenant-departments/members/target`

| Concern | Decision |
| --- | --- |
| Contract | Accept `{id}` and return only `{id,name,version}` for one department; invalid, missing, deleted, or out-of-scope IDs use shared errors. This supports known-ID assignment without department-list/read grants. |
| Authentication | JWT with an active tenant and membership context. |
| Authorization | `tenant.department:assign-member` and required department data permission; the same action protects the assignment read/write routes. |
| Operation log | None; query only. |
| Security log | None; no authentication or privilege mutation. |
| Cache | None; the version must be authoritative immediately before opening the assignment drawer. |
| Distributed lock | None; read-only. |
| Optimistic lock | The target returns the current version; the subsequent set command requires it. |
| Audit | No write; tenant, logical-delete, and data-permission scope are enforced by the shared department repository get path. |
| Presentation | Return the department name, never only an opaque ID. |
| Tests | Success, missing ID, not found/cross-tenant or scoped-out row, and route authorization registration; frontend checks malformed/mismatched targets and principal changes. |
| Shared capability | Reuse the department service, global envelope/errors, and Resource/Action interceptor; do not add a parallel list permission. |
| Dictionary provider | No; this narrow sensitive target lookup is not a dictionary. |

## `POST /api/v1/tenant-departments/members/get`

| Concern | Decision |
| --- | --- |
| Contract | Accept `{id}` and return at most 1000 current assignments containing membership/user IDs, username, display name, membership status, joined time, and primary flag. Results use stable username/display-name/ID ordering. |
| Authentication | JWT with an active tenant and membership context. |
| Authorization | `tenant.department:assign-member` with required department data permission. The service verifies the department through the same scoped get path before reading assignments. |
| Operation log | None; query only. The paired replacement command records its mutation. |
| Security log | None; reading department membership is not an authentication/session/credential event. |
| Cache | None; assignments are security-sensitive transactional state and must be current before replacement. |
| Distributed lock | None; read-only. The paired set command retains its tenant-scoped lock. |
| Optimistic lock | Not applicable to the read. The replacement command is serialized and validates authoritative membership state. |
| Audit | No writes. Department, link and membership logical-delete predicates are applied; every query requires the principal tenant ID. |
| Presentation | Related membership/user IDs always include username and display name. Joined time is returned in the platform timezone using RFC3339 JSON encoding. |
| Tests | Invalid ID, missing/denied department, tenant isolation, logical-delete filters, bounded result, inactive-member visibility, ordering and database errors. |
| Shared capability | Reuses principal, Resource/Action descriptor, data-permission compiler, response envelope and presentation timezone. |
| Dictionary provider | Not exposed: department membership is sensitive, transactional and tenant-scoped. |

