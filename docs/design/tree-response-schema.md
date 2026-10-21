# Tree response schema alignment

`POST /api/v1/permissions/tree`, `POST /api/v1/tenant-departments/tree`,
`POST /api/v1/navigations/tree`, and `POST /api/v1/me/navigations` retain their
existing behavior. This change makes their generated Swagger schemas describe
the JSON already produced by their tree builders.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Every node includes `parent_id` (`null` for a root) and `children` (an array, including `[]` for a leaf). No request, filter, ordering, pagination, error code, or route changes. |
| Authentication | Existing JWT requirements and public/internal route descriptors are unchanged; response schema does not grant access. |
| Authorization | Existing Resource/Action checks, tenant isolation, and navigation menu-capability filtering remain authoritative. |
| Operation log | Query-only tree reads retain their current no-operation-log decision. |
| Security log | No new security event; no authentication or privilege mutation occurs. |
| Cache | Existing navigation source cache stores records, not this serialized response. No new invalidation or TTL rule. |
| Distributed lock | None; reads have no cross-instance contention invariant. |
| Optimistic lock | Not applicable to these reads; mutations retain their existing expected-version checks. |
| Audit | No writes; existing audit fields remain present on management records. |
| Presentation | Explicit nullable parent and non-null child array let frontend clients use one stable tree shape. |
| Tests | Parsed Swagger contract requires both fields and checks recursive item references; runtime unit tests serialize the actual builders for all three modules. CI retains Swagger drift and existing integration gates. |
| Shared capability | All builders continue to use the platform SDK tree utility; no local tree implementation is added. |
| Dictionary provider | Permission configuration and navigation structure are unsuitable as public dictionaries; department data remains tenant-sensitive and is not exposed by this response-only change. |
