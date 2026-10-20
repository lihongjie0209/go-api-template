# Department parent presentation

| Concern | Decision and rationale |
| --- | --- |
| Contract | Existing department get/create/update/tree responses add `parent_name`; no request or error-code changes. Root nodes have an empty API value and render as “根部门”. |
| Authentication | Keep each route's existing JWT and tenant context; no public endpoint is added. |
| Authorization | Detail resolves the parent only with the same tenant ID, soft-delete predicate, and compiled department data scope as the requested department. Tree names come only from already scoped nodes. An inaccessible parent is not named. |
| Operation log | Reads add no operation event; existing create/update events remain unchanged. |
| Security log | None; this is presentation data, not a security event. |
| Cache | None; a second cache could serve stale hierarchy labels. |
| Distributed lock | None for reads; existing tree mutation lock remains unchanged. |
| Optimistic lock | Existing create/update/version behavior remains unchanged. |
| Audit | No writes or audit-field changes. |
| Presentation | The UI displays the parent name, “根部门” for roots, or “父部门不可用” when the parent is outside the permitted scope. Times remain RFC 3339 with an offset. |
| Tests | Unit tests cover scoped parent lookup, hidden parent, tree parent presentation, and UI formatting. Existing integration coverage remains service-local; no CI run for this small change. |
| Shared capability | Existing data-permission SQL scope and tree builder are reused. No new SDK component is needed. |
| Dictionary provider | No new provider: departments are transactional and tenant-scoped; existing module behavior is unchanged. |
