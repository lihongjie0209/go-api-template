# Self profile interface design

The self-profile contract is separate from platform user administration. It
always derives the target user ID from the authenticated principal and never
accepts an arbitrary user ID or account status.

| Concern | Decision |
| --- | --- |
| Contract | `POST /api/v1/profile/get` takes `{}` and returns the current `identity.User`; `POST /api/v1/profile/update` accepts `display_name`, `email`, `phone`, and `version`. Shared validation and global invalid/not-found/version-conflict errors apply. |
| Authentication | JWT only. Service accounts and system principals are rejected because they do not own an interactive user profile. |
| Authorization | Principal-scoped `identity.profile:read` and `identity.profile:update`; the service derives `id` from the trusted principal before any query or mutation. |
| Operation log | Get emits none. Update records `identity.profile.update` through the existing transactional identity recorder with a sanitized profile request. |
| Security log | Update uses the existing transactional `identity_user_changed` event because email and phone can become recovery/security identifiers. |
| Cache | Reuse `identity:user:v1:id:*` and username cache entries. Successful commit owns invalidation and refresh; no profile-specific cache is introduced. |
| Distributed lock | None. A single user row and database optimistic version are the complete contention boundary. |
| Optimistic lock | Required on update with `WHERE id=? AND version=?`; zero rows maps to the shared conflict error. |
| Audit | `database.Transactor.Within` injects the authenticated user actor; existing audit triggers/columns maintain timestamps, actor IDs, and version. |
| Presentation | RFC3339 timestamps, stable username, display name, email and phone. The frontend masks no values because the subject is viewing its own profile. |
| Tests | Unit tests prove user-principal ownership, service-account denial, status preservation, optimistic conflict, and parameterized SQL. Handler/route invariants cover the registered authorization descriptors. |
| Shared capability | Reuses identity service, transaction, cache, operation/security log, response, errors and PBAC registry. No parallel profile repository is added. |

Profile data is not a dictionary provider: it is sensitive, transactional, and
has exactly one record for the current principal rather than a bounded shared
option set.
