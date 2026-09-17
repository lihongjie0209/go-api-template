# Department member assignment interfaces

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
| Shared capability | Reuses principal, PBAC descriptor, data-permission compiler, response envelope and presentation timezone. |
| Dictionary provider | Not exposed: department membership is sensitive, transactional and tenant-scoped. |

