# Tenant member contact filters

`POST /api/v1/tenant-members/page` accepts optional exact-match `emails` and
`phones` lists (at most 200 values each). Identity owns those fields. The tenant
module asks Identity through a narrow in-process service interface for bounded
matching user IDs, then applies that ID set in the tenant-scoped SQL query
*before* count, sorting, and pagination. Identity and tenant tables are never
joined. If the identity candidate set exceeds 200, the request fails as too
broad; it must never return a partial page with a misleading total.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Extend member page filters with `emails` (case-insensitive exact) and `phones` (exact), each with at most 200 values. Empty lists mean no filter. Multiple values within one field use OR; email and phone fields, existing filters, and keyword combine with AND. |
| Authentication | Existing JWT tenant context; no public contact lookup. |
| Authorization | Existing `tenant.member:list`; compile member data scope before searching, then apply it with tenant ID to both count and item SQL. Search only returns IDs to the tenant module. |
| Operation/security logs | No operation or security event for ordinary list queries; request logging remains sanitized. |
| Cache | No contact-match cache because identity contacts change and may be sensitive. |
| Distributed lock / optimistic lock | Neither applies to a read. |
| Audit | No writes; existing audited source tables remain unchanged. |
| Presentation | Existing member names and timestamps remain unchanged. Contacts are filters, not exposed in member rows. |
| Tests | Unit cases cover exact/case-insensitive lookup, bounded candidate overflow, zero matches, ID intersection, tenant/data scope, and dependency failure; integration coverage is reserved for CI. |
| Shared capability | Reuse the Identity service query and member repository paging. Transactional membership data is not a dictionary provider. |

The bounded candidate limit is a correctness boundary, not a silent truncation.
For high-volume contact search, add a tenant-owned identity projection with a
durable update feed before relaxing this limit.
