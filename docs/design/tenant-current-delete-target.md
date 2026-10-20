# Current-tenant delete target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/tenants/delete-target` accepts `{}` and returns only `id`, `code`, `name`, `status`, `version` in the shared envelope. The ID comes from the authenticated principal, never the request. Existing `POST /tenants/delete` requires that ID and version. |
| Authentication | JWT with an active tenant context; no public or PSK exception. |
| Authorization | Both operations bind `tenant.profile:delete`; the target query additionally scopes SQL to `tenants.id = principal.tenant_id`. Platform cross-tenant administration stays separate. |
| Operation log | No log for the target query; the existing delete command records `tenant.delete` success/failure. |
| Security log | No new event for a read-only confirmation query; the existing tenant deletion security event remains authoritative. |
| Cache | No cache: the confirmation version must be current. |
| Distributed lock | None: the version predicate and transaction protect the mutation. |
| Optimistic lock | The existing delete command requires `version` and maps zero updated rows to the shared stale-version error. |
| Audit | No write in target query; existing delete transaction supplies the actor to audit triggers. |
| Presentation | Return a small safe confirmation shape with tenant name and status, no opaque owner IDs or personal details. |
| Tests | SQL-mock isolation and not-found checks, HTTP response shape and missing principal, frontend API validation and action-only entry. Integration lifecycle test belongs to CI only. |
| Shared capability | Reuse the existing JWT resource/action interceptor and response/error envelope; no new dictionary provider because tenant deletion is a sensitive transactional action. |

Permission matrix: `/tenants/delete-target` and `/tenants/delete` are JWT, `tenant.profile:delete`, tenant-scoped, and constrained to the authenticated tenant root row. The target query has no keyword, filters, sorting, or pagination because it resolves exactly one principal-owned record.
