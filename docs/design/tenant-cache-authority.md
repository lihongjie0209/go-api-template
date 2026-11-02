# Tenant detail cache authority

`tenant.Get` may use Redis to avoid loading a full row, but the tenant table
remains authoritative for visibility. A cache hit is served only after a
tenant-scoped SQL read confirms that the row is not deleted and its version
matches the cached version. A missing row or unavailable database fails closed.
An older version falls through to a fresh tenant-scoped row read.

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/tenants/get` accepts an ID and returns the existing tenant view; invalid IDs, missing rows and dependency errors keep their global mappings. This change does not alter page filters or sorting. |
| Authentication | JWT with an active tenant context; the existing endpoint descriptor remains authoritative. |
| Authorization | Existing tenant Resource/Action check runs before the handler. Both the version and full-row SQL queries require `id = requested ID AND id = principal tenant ID`, and reject logically deleted rows. |
| Operation log | No new event: this ordinary detail read is not an export or security-sensitive log query. |
| Security log | No new event: no credential, membership or privilege state is changed. |
| Cache | Keep the existing `tenant:v1:<tenant ID>:<ID>` key and configured TTL. The owning tenant mutation invalidates after commit; a missed invalidation cannot bypass the authoritative version and delete check. A cache miss or invalid value falls back to SQL. |
| Distributed lock | None: the read has no shared mutation invariant. |
| Optimistic lock | No client version is needed for a read; mutation endpoints retain their `WHERE id AND version` predicates. |
| Audit | Read only; PostgreSQL audit triggers continue to maintain mutation fields. |
| Presentation | The existing tenant view supplies owner and actor names and +08 timestamps. |
| Tests | Unit regressions cover deleted cached rows, stale versions and authoritative database outage. Tagged database integration remains in the service CI suite. |
| Shared capability | Reuses the shared cache Store and presentation helpers; the small authoritative version query belongs to the tenant repository. |

The tenant root is transactional and context-sensitive, so it is not registered
as a public data-dictionary provider.
