# Platform tenant update target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/platform/tenants/update-target` accepts `{ "id": "..." }` and returns only `{id,code,name,description,status,version}` in the shared envelope. An existing `/platform/tenants/update` request carries the same ID and version. |
| Authentication | Platform JWT; no anonymous, tenant-scoped, or PSK path. |
| Authorization | Both target and write bind canonical `tenant:update`; the service also requires a platform principal. The target uses only the supplied exact ID and excludes soft-deleted rows. |
| Operation log | No operation log for the narrow read; the existing platform update emits `tenant.admin.update`. |
| Security log | None: ordinary tenant profile edit does not change credentials, membership, or privilege grants. |
| Cache | Target bypasses cache for a fresh optimistic version. Existing update owns invalidation. |
| Distributed lock | None: the versioned transaction and constraints provide the concurrency boundary. |
| Optimistic lock | The existing update requires `id` and positive `version`; conflicts are surfaced, never retried implicitly. |
| Audit | Existing update transactor injects the platform actor and database triggers maintain audit columns. |
| Presentation | Response includes only editable fields and the stable tenant code; status uses the existing label mapping. No related IDs or timestamps are exposed. |
| Tests | Unit tests cover platform principal, invalid ID, fresh exact-ID query, missing row, database failure, handler response and validation; integration lifecycle covers version freshness and soft deletion. |
| Shared capability | Reuses the resource/action interceptor, standard envelope, tenant repository and common editor. No SDK extraction needed. |
| Dictionary provider | Tenant profile is sensitive transactional data, not a dictionary. |

The platform page must be reachable with `tenant:update` alone. It must not use
the read/list routes to fetch the edit version, nor widen tenant-level access.
