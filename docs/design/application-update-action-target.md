# Application update-only target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/applications/update-target` accepts `{id}` (canonical UUID) and returns only immutable code, editable fields and version. Errors use the shared envelope and invalid/not-found codes. No pagination. |
| Authentication | JWT; route is bound to `application:update`, not `application:read`. |
| Authorization | Platform application update action; the route interceptor checks grants before the handler. Deleted records are invisible. |
| Operation/security log | Lookup logs neither; the existing versioned update records its operation. No credential or privilege event is created here. |
| Cache/lock | No cache: editor needs the authoritative current version. No distributed lock: the existing optimistic version predicate protects updates. |
| Optimistic lock/audit | Target exposes `version`; submit uses existing `WHERE id AND version` transaction and audit actor trigger. |
| Presentation | Editable fields are returned without audit or actor details; code is a stable, non-editable identity. |
| Tests | Unit tests cover principal, invalid ID, missing row, dependency failure and selected fields; handler test covers response. The application lifecycle integration test asserts fresh targets before and after update when CI next runs. |
| Shared capability/dictionary | Reuse route authorization, response and editor components. Application is transactional, so no new dictionary provider. |
