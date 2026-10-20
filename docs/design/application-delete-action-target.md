# Application delete action target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/applications/delete-target` accepts a canonical application UUID and returns only `{id,code,name,status,version}` in the shared envelope. The existing delete command accepts the same ID plus a positive expected version. Missing/deleted IDs return the global not-found error; malformed IDs return invalid input. |
| Authentication | JWT; neither public nor PSK. |
| Authorization | Both routes bind `application:delete` at registration. The target does not require `application:read/list`; the delete command independently rechecks authorization. |
| Operation log | Target read: none. Existing delete command records the application name and version, with failure status when rejected. |
| Security log | None: application deletion is an operation event, not an identity/privilege event. |
| Cache | No target cache: the version and status must be authoritative. Committed deletion invalidates the existing application/navigation frontend cache. |
| Distributed lock | None: versioned SQL, navigation/grant reference checks, and database constraints provide the integrity boundary. |
| Optimistic lock | The target's version is submitted to delete; zero affected rows map to the global stale-version error. A stale result is not retried automatically and requires a fresh target lookup. Existing navigation or tenant grants retain the distinct business-conflict error. |
| Audit | Target is read-only; the existing delete transaction uses the context actor and audit trigger for soft deletion. |
| Presentation | Show code, name, status, and version before confirmation. No metadata or unrelated tenant grants are returned. |
| Tests | Go sqlmock cases cover minimal query, invalid/missing IDs, DB failure, stale version, and reference conflicts; handler and Swagger route checks cover response shape and authentication; frontend API, page and mounted real-capability tests cover delete-only access, stale input/principal, version, failure and post-commit local-row/cache invalidation. A Testcontainers lifecycle assertion is added for CI but not run locally. |
| Shared capability | Reuse the existing action interceptor, response/error packages, `GoReadByIDPanel`, `useKnownActionTarget`, and `GoAuthorizedActionButton`. No dictionary provider: applications have a separate owned selector and mutable lifecycle. |

| Route | Principal | Resource/action | Scope | Data filtering |
| --- | --- | --- | --- | --- |
| `/applications/delete-target` | JWT | `application:delete` | Platform application | Existing, non-deleted applications only |
| `/applications/delete` | JWT | `application:delete` | Platform application | Existing versioned command and reference checks |
