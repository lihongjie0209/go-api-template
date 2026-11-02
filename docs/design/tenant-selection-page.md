# Bounded tenant selection

The existing `POST /api/v1/tenant-context/available` remains a selector for
users with a modest tenant set. It reads at most 1,001 rows and returns a
clear error if the active set exceeds 1,000. The new
`POST /api/v1/tenant-context/available/page` supports bounded paging for
larger sets without changing the existing response shape.

| Concern | Decision and rationale |
| --- | --- |
| Contract | The new page request accepts `page`, `page_size`, literal case-insensitive `keyword` and up to 200 `tenant_ids`; it returns `{items,page,page_size,total}`. Page size uses shared limits. The legacy list is capped at 1,000 and reports that the paged API is needed when exceeded. Sorting is fixed by tenant name and ID for stable selection; status is active-only. |
| Authentication | JWT user principal with an active session; no active tenant is required because selection precedes tenant context. |
| Authorization | Both routes bind `tenant.selection:list`. SQL joins only active, undeleted memberships belonging to the authenticated user and active, undeleted tenants. The optional ID filter narrows this set. |
| Operation log | None: ordinary selector reads do not change state or export sensitive content. |
| Security log | None for reads; the separate switch operation retains its security event. |
| Cache | None: active tenant and membership state is authoritative on every selection. |
| Distributed lock | None: reads have no contention invariant. |
| Optimistic lock | No mutation; tenant/member updates continue to use expected versions. |
| Audit | SQL reads the database-owned `joined_at`; mandatory audit fields remain on the underlying tables. |
| Presentation | Tenant and membership display names are returned, and `joined_at` uses the shared +08 response conversion. |
| Tests | Unit tests cover bounded legacy output, user scope, literal keyword escaping, page metadata, ID filters, invalid bounds and +08 presentation. Tagged database tests remain a CI-only service gate. |
| Shared capability | Reuses pagination and `database.LikeContainsPattern`; no dictionary provider because the selectable set is principal-specific and transactional. |

The selector does not expose a joined-at range filter because users select by
tenant identity and name; `joined_at` is only a display field. The optional
ID list is an exact `IN` filter, while code and name participate in literal
case-insensitive keyword search. An empty ID list means no ID restriction.
