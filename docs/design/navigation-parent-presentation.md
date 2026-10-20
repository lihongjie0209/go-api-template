# Navigation parent presentation

| Concern | Decision and rationale |
| --- | --- |
| Contract | Existing navigation get/create/update/tree responses add `parent_name`; no request or error-code changes. Root nodes return an empty value and the UI displays “根节点”. |
| Authentication | Keep the existing JWT and canonical navigation Resource/Action descriptors. |
| Authorization | Management get resolves a parent only in the same application and only when it is not deleted. The current-tenant tree keeps its existing tenant-grant and membership SQL gate; the HTTP layer still prunes unauthorized navigation before returning it. |
| Operation log | Reads add no operation event; create/update events remain unchanged. |
| Security log | None; this is presentation metadata. |
| Cache | Existing revisioned current-navigation source cache stores raw records. Parent names are derived after loading so cache invalidation and keys do not change. |
| Distributed lock | None for reads; mutation behavior is unchanged. |
| Optimistic lock | Existing update/delete version predicates remain unchanged. |
| Audit | No writes or audit-field changes. |
| Presentation | Management details display the parent name, “根节点” for roots, or “父导航不可用” if a referenced parent is unavailable. Current-tree names are derived only from its authorized source tree. |
| Tests | Unit tests cover direct lookup, same-application/non-deleted parent boundary, and tree parent names. Frontend tests cover the display states. Existing integration tests remain CI-only. |
| Shared capability | Reuse the existing tree builder and presentation flow; no new SDK component is needed. |
| Dictionary provider | No new provider: navigation is application configuration, not bounded selectable dictionary data. |

## Application target for navigation-only operators

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/navigations/application-target/{read,list,create}` accepts `{id}` (non-empty, at most 128 bytes) and returns only `{id,name}` for one non-deleted application. Invalid and unknown IDs use the global validation/not-found errors; no pagination applies. |
| Authentication | JWT platform principal; no anonymous or PSK access. |
| Authorization | The three explicit route descriptors bind `navigation:read`, `navigation:list`, and `navigation:create` respectively. The UI selects a route from an allowed action, so navigation-only operators do not need unrelated `application:read`. Navigation mutations remain authorized independently. |
| Operation log | None for this bounded read; navigation mutations retain their operation events. |
| Security log | None; this lookup changes no security state. |
| Cache | None; the application display name comes from the authoritative row. |
| Distributed lock | None; read-only. |
| Optimistic lock | No write or version comparison. |
| Audit | No writes; the lookup excludes soft-deleted applications. |
| Presentation | The route page validates the returned ID and non-empty name before showing the application title. It clears a previous title on route/principal changes. |
| Tests | Service validation and missing-row unit cases, handler binding/response, Swagger route coverage, and mounted frontend tests for navigation-only lookup, route mismatch, and cancellation. |
| Shared capability | Reuse the existing application service, Resource/Action route registration, capability evaluation, and verified-route reference composable. |
| Dictionary provider | None; this is a privileged administrative target lookup, not reference-data search. |
