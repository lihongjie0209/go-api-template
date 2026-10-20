# User delete action target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/users/delete-target` accepts `{"id":"canonical UUID"}` and returns only `{id,username,display_name,status,version}` in the shared envelope. Missing, closed, or deleted users return the global not-found code; malformed IDs return invalid input. The existing `/users/delete` still requires `id` and positive expected `version`. |
| Authentication | JWT only; no public or PSK access. |
| Authorization | Both target and mutation bind `identity.user:delete`. The target does not require `identity.user:read/list`. The shared route interceptor checks before the handler; the mutation rechecks authorization independently. |
| Operation log | Target read emits none; the existing user delete command records its operation with a safe name/version summary. |
| Security log | Target read emits none; the existing delete command retains its security event and session revocation. |
| Cache | No target cache: version and status must be authoritative. Existing user cache invalidation stays owned by the delete command after commit. |
| Distributed lock | None: versioned SQL update and reference constraints are authoritative; a global user lock would add no necessary invariant. |
| Optimistic lock | Delete submits the target's positive version; stale writes return the shared conflict code. After a conflict, the UI discards the target and requires a fresh lookup. |
| Audit | Target is read-only. The existing delete transaction derives the actor from context and maintains audit/soft-delete fields. |
| Presentation | Show username, display name, status, and version before confirmation; never expose email, phone, password, or sessions. Deleting the signed-in user clears its revoked local session after commit. |
| Tests | SQL-mock unit tests for minimal fields, canonical IDs, missing users, and database failures; handler/route tests for narrow response and same-action binding; frontend API/page and mounted real-capability tests for delete-only access, changed input, conflict, principal switch, and committed-session cleanup. Add a Testcontainers lifecycle assertion for the later CI integration run. |
| Shared capability | Reuse identity repository conventions, shared errors/envelope/interceptor, `useKnownActionTarget`, `GoReadByIDPanel`, and `GoAuthorizedActionButton`. No new data-dictionary provider: user deletion is sensitive transactional data. |

| Route | Principal | Resource/action | Scope | Data filtering |
| --- | --- | --- | --- | --- |
| `/users/delete-target` | JWT | `identity.user:delete` | Platform user object | Global identity user, non-deleted and non-closed |
| `/users/delete` | JWT | `identity.user:delete` | Platform user object | Existing versioned delete and reference checks |
