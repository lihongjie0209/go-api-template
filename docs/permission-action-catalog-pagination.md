# Permission action catalog paging decision

`POST /api/v1/authorization/actions/page` supplies the permission-action assignment drawer. The drawer pages the catalog within the selected permission resource's scope instead of downloading every action. The existing `POST /api/v1/authorization/resources/page` resolves the selected permission's resource by exact `resource_key`.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Add optional `resource_key`, `scope`, and `active_resources_only` to the catalog page request. Action pages apply all three in SQL to both count and items. Page stays one-based, maximum 200; `keyword` searches action key/name and parent resource key/name case-insensitively, and composes with exact resource/scope/status filters. Invalid scope/key returns the global invalid-input error. Resource pages accept exact key and scope but reject `active_resources_only`, which has no meaning without a parent resource. |
| Authentication | JWT; neither page is public or PSK. |
| Authorization | `authorization.action:list` and `authorization.resource:list` remain required. These are platform administration catalogs; the selected permission's scope is a query filter, not a grant. The mutation still requires `permission.definition:update`. |
| Operation log | None for page reads; the existing permission-action mutation remains logged by its owner. |
| Security log | None for page reads; changing an action binding retains the existing privilege-change security event. |
| Cache | None; the catalog changes on code reconciliation and manual edits, and paging avoids the prior unbounded client snapshot. |
| Distributed lock | None; reads do not protect a cross-instance invariant. |
| Optimistic lock | Not applicable to reads; `/permissions/actions/set` still requires the permission version. |
| Audit | Reads do not mutate audit fields. Catalog writes continue through their existing transactor and triggers. |
| Presentation | Action results carry `resource_name`, `resource_key`, `action_key`, and name for a display label; no opaque ID is shown alone. No timestamp transformation is added. |
| Tests | Unit tests assert valid/invalid scope and key, SQL placeholder binding, and identical count/list filtering. The existing Testcontainers lifecycle adds matching and mismatching scope queries; CI runs it later. Frontend unit tests cover paged search, stale selection, and validation before versioned save. |
| Shared capability | Reuses the catalog page API and frontend `GoSelectionDrawer`; no new infrastructure abstraction. |

The action catalog is not a public dictionary provider: it is administrative, authorization-sensitive, and can grow without a small fixed bound. The picker sends only the currently selected IDs (at most 100) for a final active/scope check before mutation. The backend repeats all authoritative checks.
