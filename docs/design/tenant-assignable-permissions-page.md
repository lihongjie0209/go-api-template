# Tenant-assignable permission paging

`POST /api/v1/tenant-authorization/assignable-permissions/page` is the bounded candidate source for role creation and assignment. The existing unpaged endpoint remains for compatibility; new UI code uses this page endpoint. The query applies the same active membership, administrator, tenant-ceiling, and active-role rules as assignment validation. Optional `ids` (at most 200) lets the UI classify selected legacy grants without loading the full ceiling.

| Concern | Decision and rationale |
| --- | --- |
| Contract | POST JSON `{page,page_size,keyword,ids}`; default page size 20, max 200; `ids` max 200; keyword max 256. Return `{items,page,page_size,total}` ordered by permission key and ID. Invalid bounds use the shared invalid-request code. Exact `ids` and case-insensitive permission key/name search combine with AND. Empty `ids` means no ID filter; empty result is `items: []`. |
| Authentication | Tenant JWT only. A missing or inactive tenant membership is denied. |
| Authorization | Registered `tenant.authorization:read`; SQL intersects active tenant ceiling with the caller's active role grants, except a tenant administrator may see the whole ceiling. The same count/list predicate requires an active tenant-scoped Resource/Action in the database; both code-owned and manual registrations are eligible. The setter uses that same database condition and rechecks the ceiling. The route descriptor is registered beside the handler. |
| Operation log | None; query-only and returns non-sensitive permission metadata. |
| Security log | None; no credential, membership, or privilege mutation. |
| Cache | None; grant revocation should be visible immediately and the bounded query avoids a full-set cache. |
| Distributed lock | None; query-only. |
| Optimistic lock | Not applicable to a read. The assignment write retains role-version validation. |
| Audit | No write; existing audited permission/grant tables remain authoritative. |
| Presentation | Permission key, name, resource, and action are returned together; no timestamps or opaque-only references. |
| Tests | Unit tests cover administrator/member SQL scope, active tenant Resource/Action filtering, manual Resource/Action eligibility, selected-ID filtering, invalid bounds, and inactive membership; frontend unit tests cover page/search and old grant display. A database-backed integration case is in the service's `integration` suite and will run in CI at the next major feature gate. |
| Shared capability | Reuses shared pagination, response/error envelope, and registered authorization descriptor. No new SDK primitive is needed. |
| Dictionary provider | No: these are security-sensitive, rapidly changing permission grants, not a public dictionary. |

UI candidate lists are hints only. The write path repeats authoritative assignment validation and optimistic-version checks.
