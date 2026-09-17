# Application and Navigation Modules

## Domain model

- An application is a platform-owned deployable/product boundary identified by
  an immutable lowercase `code` and deterministic UUID v5.
- A navigation belongs to exactly one application and is either `directory` or
  `menu`.
- Directories organize children and never carry a component or authorization
  target.
- Menus are leaves. They carry a frontend route/component and may bind exactly
  one canonical PBAC `resource + action` pair. Resource/action values are
  validated against the code-owned registry; HTTP paths are not authorization
  identities.
- Parent and child must belong to the same application. Cycles, missing parents,
  menu parents, and deleting non-leaf nodes are rejected.

## Interface design review

| Concern | Application decision | Navigation decision |
| --- | --- | --- |
| Contract | POST create/get/page/update/delete with bounded filters and sort allowlist | POST create/get/tree/update/delete; tree requires `application_id`, is non-paginated, ordered, and bounded |
| Authentication | JWT | JWT; current-user projection uses the same authenticated principal |
| Authorization | Platform `application:<action>` | Platform `navigation:<action>`; `/me/navigations` uses tenant `navigation.current:read`, then batch-evaluates each menu Resource/Action |
| Operation log | Record create/update/delete | Record create/update/delete and move |
| Security log | None; no credential or grant changes | None; navigation metadata does not grant authority |
| Cache | None; management reads are not hot | `/me/navigations` caches only the application-scoped active/visible source records in the shared Redis Store for `navigation.cache_ttl` (default 5m). Every request still validates active membership, tenant grant validity, and application status in SQL, derives the cache key from the authoritative navigation version sum/count, and re-evaluates PBAC for the current principal. Old revision keys become unreachable and expire; cache failure falls back to SQL. Final principal decisions are never cached. |
| Distributed lock | None; code uniqueness and optimistic version are authoritative | None; serializable transaction, FK checks, cycle validation, and optimistic version protect mutations |
| Optimistic lock | Update/delete require version | Update/delete/move require version |
| Audit | Shared transaction actor and database audit triggers | Shared transaction actor and database audit triggers |
| Presentation | RFC3339 +08 timestamps and actor display names | Same; Resource/Action are semantic display fields rather than opaque IDs |
| Tests | Validation, filtering, not-found, conflict, cancellation; Testcontainers repository coverage | Type/target matrix, parent/application/cycle/leaf rules, stable tree order; Testcontainers repository coverage |
| Shared capability | Shared pagination, response/errors, stable ID, operation log | Shared PBAC registry, tree SDK, stable ID, response/errors, operation log |
| Dictionary | Suitable as a bounded application selector provider in a later delivery | Unsuitable: navigation is structural frontend configuration, not a business dictionary |

Application and navigation are platform-scoped configuration. Tenant grants are
implemented as the separate tenant-owned `tenant_application_grants`
association documented in `tenant-application-design.md`; neither platform
table carries a nullable tenant column.

Migration `000031` retires the legacy global `menus` table. The old menu HTTP,
DI, configuration, and PBAC registrations are removed so navigation has one
authoritative model.
