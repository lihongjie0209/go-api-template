# Navigation route-path uniqueness

Applies to `POST /api/v1/navigations/create` and `POST /api/v1/navigations/update`.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Keep existing create/update models and error codes. Reject a route path whose final application-relative URL matches another navigation node, including paths produced by nesting. Paths are case-insensitive for collision checks, matching the frontend router's default matching. |
| Authentication | JWT; the existing navigation create/update descriptors remain required. |
| Authorization | Existing platform navigation Resource/Action checks run before mutation. This validation cannot grant access. |
| Operation log | Keep the existing success/failure navigation mutation event, without embedding unrelated menu data. |
| Security log | None; this changes route configuration, not credentials or direct grants. The existing authorization revision refresh remains mandatory. |
| Cache | Successful commits keep the existing navigation cache invalidation/revision behavior; rejected mutations do not invalidate it. |
| Distributed lock | None; the serializable transaction and application-row lock serialize configuration changes. |
| Optimistic lock | Update still requires the expected navigation version. |
| Audit | Existing transaction-local actor and table triggers maintain audit fields. |
| Presentation | The frontend keeps configured menu URLs; it rejects historical duplicate URLs before installing ambiguous routes and shows a repair message. |
| Tests | Pure tree tests cover sibling and nested collisions plus distinct paths; frontend runtime menu tests cover stale duplicate data. Local runs use unit tests only. |
| Shared capability | Reuse the existing navigation tree construction and resource-page editor; no parallel route registry is introduced. |
| Dictionary provider | Navigation is mutable application configuration, not a bounded dictionary. |

The canonical path is assembled from every ancestor directory and the node's
`route_path` (or `navigation_key` when no route path is configured). The URL
must be unique across all active and disabled nodes so enabling a previously
disabled entry cannot silently shadow another menu.

`/` is not a valid configured route path: it contains no segment, while the
runtime router would otherwise fall back to `navigation_key` and serve a URL
different from the one entered by an administrator. A directory may leave
`route_path` empty to use its key; a menu must provide a non-root path.
