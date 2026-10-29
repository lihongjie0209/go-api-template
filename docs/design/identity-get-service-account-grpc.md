# Identity GetServiceAccount gRPC design

`platform.identity.v1.IdentityService/GetServiceAccount` returns a non-secret
service-account summary to an authorized internal caller. It does not issue a
credential or enumerate accounts.

| Concern | Decision and rationale |
| --- | --- |
| Contract | `service_account_id` is required and bounded by the existing service-account ID rule. Return `id`, `name`, `enabled`, and `audiences`. Empty/malformed IDs map to `InvalidArgument`, deleted/missing accounts to `NotFound`, and store failures to `Unavailable`. No pagination, sorting, or filters. |
| Authentication | Internal service principal through the existing gRPC authentication interceptor. |
| Authorization | Canonical `identity.service-account:read`, platform scope, checked before the handler. The read is limited to the requested account ID and excludes logically deleted rows. |
| Operation log | None: this is a bounded, non-secret read. |
| Security log | None: it does not authenticate, issue a token, or change a credential. |
| Cache | None: `enabled` must reflect current status, expiry, and lock state. |
| Distributed lock | None: read-only. |
| Optimistic lock | Not applicable: no write. |
| Audit | No fields are changed. |
| Presentation | Return the stable account ID with its display name. `audiences` is the platform's single configured JWT audience, not an account-specific grant list; disabled accounts retain that declared audience but cannot authenticate. No timestamps or secrets are exposed. |
| Tests | Unit tests cover active, disabled, expired, locked, missing, invalid, missing-principal, and database-failure cases. Existing service-account Testcontainers tests cover authoritative account reads on PostgreSQL and MySQL. |
| Shared capability | Reuses the existing service-account service, principal and resource/action interceptor. No local account repository or authorization middleware. |
| Dictionary provider | None: service accounts are sensitive transactional identities. |
