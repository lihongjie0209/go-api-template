# Tenant role assignment candidates

`POST /api/v1/tenant-roles/assignment-candidates/page` returns active, tenant-scoped roles visible to the caller, with an `assignable` flag. This is a UI hint; `tenant-members/roles/set` rechecks every role in its transaction.

| Concern | Decision and rationale |
| --- | --- |
| Contract | POST JSON `{page,page_size,keyword,ids}`; standard page envelope with role fields and `assignable`. IDs are bounded to 200; normal pagination and keyword limits apply. |
| Authentication | JWT; no public or PSK route. |
| Authorization | `tenant.role:list`, tenant data scope, plus active membership; candidates satisfy the same permission-subset rule as role assignment. |
| Operation/security logs | Neither; read-only metadata, no sensitive credential data. Assignment mutation retains its own logging. |
| Cache/lock/optimistic lock | None for this read; role/grant changes must be immediately reflected. Transactional assignment remains authoritative and versioned. |
| Audit | Read-only; existing role audit fields are returned by the shared presentation path. |
| Presentation | Role name/code/status plus explicit assignability; dates retain platform RFC 3339 presentation. |
| Tests | Unit tests cover active membership, tenant/data scope, valid/invalid role permissions, empty roles, and failure propagation. Integration remains a CI-only Testcontainers gate. |
| Shared capability | Reuses the existing role page, principal, database Resource/Action catalog, pagination, response, and data-scope mechanisms. Manual tenant actions remain eligible when active. No dictionary provider: roles and delegation are security-sensitive transactional data. |

`assignable=false` keeps an already-assigned but no-longer-delegable role visible and removable. Pagination counts all active visible candidates, not just assignable ones; filtering after pagination is forbidden.
