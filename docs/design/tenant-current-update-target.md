# Current tenant update target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/tenants/update-target` accepts `{}` and returns only `id`, `code`, `name`, `description`, `status`, `version` in the standard envelope. The current tenant comes from the authenticated principal; no caller-supplied ID is accepted. Existing `/tenants/update` performs the versioned write. |
| Authentication | JWT; no public or PSK access. |
| Authorization | `tenant.profile:update` on both target and write. The repository limits the target query to `tenants.id = principal.tenant_id` and non-deleted rows. No `tenant.profile:read` or list grant is needed. |
| Operation log | No log for target read; existing update records `tenant.update`. |
| Security log | None for ordinary profile edit; no credential or privilege change. |
| Cache | No cache for target; fetch fresh version directly from PostgreSQL. Existing update owns invalidation. |
| Distributed lock | None; version predicate and transaction enforce concurrent edit safety. |
| Optimistic lock | Existing update requires positive `version` and reports stale-version conflict. |
| Audit | Read is unchanged; existing update transactor injects actor and database trigger maintains audit fields. |
| Presentation | Editable fields are narrow; `status` has the existing UI label mapping. No opaque related ID or timestamp is exposed. |
| Tests | Unit tests cover principal scoping, missing tenant, missing row, backend failure, exact response shape and malformed JSON; service integration lifecycle covers freshness. |
| Shared capability | Reuses existing handler envelope, resource/action interceptor, tenant repository, and editor contract. No new SDK capability. |
| Dictionary provider | Tenant profile is sensitive transactional data, not a dictionary. |

This route exists so an operator granted only `tenant.profile:update` can load
the current editable values and optimistic version without gaining the broader
profile read or tenant list capability.
