# Tenant Application Authorization

An application remains platform-owned. A tenant grant is the only authority
that makes an active application available inside that tenant. A grant is a
tenant-owned association and every read or mutation includes `tenant_id`.
Revocation is an explicit state transition; rows are retained for audit and may
be reactivated only with the current optimistic version.

## Interface design review

| Concern | Decision |
| --- | --- |
| Contract | Platform POST `grant`, `revoke`, `page`; principal POST `/me/applications`. Grant uses version `0` for first creation and the current positive version for reactivation/update. Revoke requires ID, tenant ID and version. Page uses bounded keyword/ID/application/status/time filters and an allowlisted stable sort. |
| Authentication | JWT for every interface. Platform mutation/page requires a platform principal; current applications requires an active user tenant and membership context. |
| Authorization | Platform operations use `tenant:grant-application` and `tenant:read-applications`. Current selection uses tenant-scoped `application.current:list`. Navigation projection additionally requires an active, unexpired grant for its requested application. |
| Operation log | Grant/reactivate and revoke record success atomically and failure asynchronously with safe summaries. |
| Security log | Grant and revoke emit `tenant_application_authorization_changed`; a failure to enqueue follows the shared fail-closed policy. |
| Cache | None in this delivery. Current selection is a bounded indexed join. Revision-keyed caching is deferred until measured because grant, application and PBAC revisions must all participate. |
| Distributed lock | None. The unique `(tenant_id,application_id)` constraint, serializable transaction and expected version own the invariant. |
| Optimistic lock | Reactivation/expiry change and revoke require the current grant version. First creation requires version zero and conflicts with any historical row. |
| Audit | Shared transaction actor plus mandatory database audit triggers. No physical delete. |
| Presentation | Current results contain application ID/code/name/icon/home path plus grant expiry; RFC3339 timestamps render in Asia/Shanghai. No opaque application ID is returned without its display fields. |
| Tests | Unit validation, tenant-context rejection, SQL tenant predicate, stale versions, current filtering and mutation logging; PostgreSQL/MySQL Testcontainers lifecycle under the integration tag. |
| Shared capability | Reuses transaction, principal, pagination, PBAC, response/error, operation/security log and presentation components. |
| Dictionary | Applications remain suitable for the bounded application-selector provider; the current-principal interface is authorization-sensitive and is not a public dictionary. |

