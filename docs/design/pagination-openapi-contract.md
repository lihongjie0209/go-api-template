# Concrete OpenAPI pagination models

Swaggo v1.16.6 rendered several `pagination.Result[T]` aliases as empty object
schemas. The following POST+JSON responses now use concrete result structs with
the same runtime JSON fields: applications, tenant application grants, platform
permission grants, authorization resources/actions, scheduled job definitions,
and scheduled job runs.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Preserve the existing `{items,page,page_size,total}` body in the shared response envelope, page defaults, filter/sort allowlists, and error codes. Concrete models expose typed `items` references in generated Swagger; no route or request changes. |
| Authentication | Existing JWT route descriptors are unchanged. |
| Authorization | Existing Resource/Action checks and tenant/data scope remain authoritative; a documentation fix grants no access. |
| Operation log | Read-only pages retain their existing no-operation-log decisions. |
| Security log | No new security event; these reads do not mutate credentials, sessions, memberships, or grants. |
| Cache | Existing owner-specific cache decisions remain unchanged; no response cache is added. |
| Distributed lock | None; paginated reads have no cross-instance contention invariant. |
| Optimistic lock | Not applicable to reads; mutation endpoints retain expected-version checks. |
| Audit | No writes. Returned record audit fields and the shared transaction actor contract are unchanged. |
| Presentation | Typed item records, related display names, and RFC 3339 timestamps remain in the same response shape; empty result lists serialize as `[]` when constructed by the services. |
| Tests | A parsed Swagger test checks all seven repaired page models and each route's envelope body reference. A second parsed contract iterates every `/page` route (32 at the time of this audit) and requires a POST success body with typed `items`, `page`, `page_size`, and `total`. Runtime JSON and resource-catalog service tests check the empty-page `items: []` shape. Existing module unit and CI Testcontainers suites continue to cover query semantics. |
| Shared capability | Keep the shared pagination request/normalization helpers. Concrete output types are needed at the OpenAPI boundary because the current generator cannot resolve generic aliases; they do not reimplement pagination behavior. |
| Dictionary provider | This response-model change does not make sensitive permission, grant, job, or resource data public dictionaries. Application display metadata retains its separately reviewed provider. |
