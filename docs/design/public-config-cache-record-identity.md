# Public platform configuration cache identity

| Concern | Decision and rationale |
| --- | --- |
| Contract | The existing public `GetPublic` operation accepts a validated configuration key and returns the shared envelope with `PublicView`; it does not change its HTTP request, response, or error codes. No pagination, sort, or filters apply to a single-key lookup. |
| Authentication | Public and anonymous by design. The database must confirm `is_public`, active status, and non-deletion before every cache hit. |
| Authorization | No principal, tenant, or object grant applies; publication is the explicit platform-level authorization decision. A private or removed record returns not found. |
| Operation log | None: this is a public, query-only read and contains no sensitive configuration values by validation. |
| Security log | None: it does not modify authentication, credentials, sessions, or privileges. |
| Cache | Redis key `platform-config:public:v3:<key>:<record-id>:<version>` uses database-derived identity and configured TTL. PostgreSQL/Kingbase can replace a soft-deleted row with another ID and the same version, so key and version alone are insufficient. Each request reads authoritative visibility first; a cache failure falls back to the database, and a database failure fails closed. Values are loaded by ID, key, version, and visibility conditions to handle a concurrent replacement. Old v2 keys expire without reuse. |
| Distributed lock | None: reads do not establish a cross-instance invariant; immutable versioned cache entries avoid write contention. |
| Optimistic lock | Not applicable to reads. Existing writes retain version-checked predicates and trigger-managed version increments. |
| Audit | No write occurs. Existing records retain all mandatory audit fields and their database triggers. |
| Presentation | `PublicView` retains its existing JSON value and value-type shape; it exposes no internal ID or timestamp. No related ID needs display resolution. |
| Tests | Unit regression reproduces two records with one key and version but different IDs, proving the second returns its own value. Existing tests cover privacy changes, version races, cache failure, and database failure. The existing service lifecycle remains covered by tagged Testcontainers integration tests in CI. |
| Shared capability | Reuse the shared `cache.Store`, database adapter, and safe logging. Public platform configuration is not a data-dictionary provider because it is operational configuration rather than bounded selectable business reference data. |
