# Forced password change design

An administrator password reset creates a temporary credential. The user may
authenticate with it, but the resulting session may only change the password,
refresh that restricted session, or log out until the user chooses a new
password.

| Concern | Decision |
| --- | --- |
| Contract | `POST /api/v1/auth/user/login` and `POST /api/v1/auth/refresh` return `must_change_password`; `POST /api/v1/auth/password/change` requires `old_password` and `new_password`. Invalid credentials use the shared authentication error mapping. |
| Authentication | Login and refresh remain public credential exchanges. Password change and logout require the restricted JWT. |
| Authorization | A JWT claim records the immutable password state at issue time. HTTP middleware fails closed for a restricted user token except password change, refresh, and logout. PBAC still authorizes each allowed operation. |
| Operation log | None. Credential changes use the independent security log rather than duplicating an operation log. |
| Security log | Administrator reset records `password_reset`; successful self-service change records `password_changed`; login, refresh, and logout keep their existing security events. |
| Cache | None. Credential state is authoritative database data and is copied only into bounded-lifetime signed access tokens. Password reset/change revokes existing sessions. |
| Distributed lock | None. The credential row is locked with `SELECT FOR UPDATE`; optimistic versioning and the database transaction are the contention boundary. |
| Optimistic lock | Credential updates retain `WHERE id=? AND version=?`; zero affected rows fail the operation. |
| Audit | The existing audited credential table gains `must_change_password`. Writes run through `database.Transactor.Within`; triggers maintain actor, timestamps, and version. |
| Presentation | The login screen says “username”, and the password-change form asks for current password, new password, and confirmation. No credential value is logged or returned. |
| Tests | Unit tests cover reset setting the flag, self-change clearing it, login/refresh propagation, JWT claim round-trip, middleware allow/deny behavior, and the real mounted frontend form payload. Integration migration execution remains in GitHub CI. |
| Shared capability | Reuses authentication, JWT, principal context, global response/errors, database transaction, and security-log infrastructure. No service-local authorization mechanism is introduced. |

The credential is sensitive transactional data and is not a data-dictionary
provider. The state is not tenant-owned: usernames and credentials belong to
the global identity boundary.
