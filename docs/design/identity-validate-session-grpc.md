# Identity ValidateSession gRPC design

`platform.identity.v1.IdentityService/ValidateSession` is a read-only, internal
service-to-service operation. The gRPC registration binds the canonical
`identity.internal-authentication:validate-session` resource/action and requires
a service principal; the operation authorization interceptor fails closed.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Request `session_id` and `user_id` are required and bounded to 256 bytes. Response is `valid` plus `expires_at` only when valid. Invalid/revoked/expired sessions return `valid=false`; malformed input returns `InvalidArgument`, state-store failure returns `Unavailable`. No pagination or sorting. |
| Authentication | Internal service principal over the configured gRPC authentication channel; not public and not a JWT-only user endpoint. |
| Authorization | `identity.internal-authentication:validate-session`, platform scope. No tenant data is returned. The caller must have the operation grant; the handler does not infer permission from a session ID. |
| Operation log | None: a high-volume, read-only internal probe with no state transition. |
| Security log | None per probe: probing is not a login, token issue, or credential change. Suspicious caller behavior belongs to the gateway/rate-limit security flow. |
| Cache | None: revocation and account status must be checked against authoritative state. |
| Distributed lock | None: read-only and no cross-instance mutation invariant. |
| Optimistic lock | Not applicable: no write. |
| Audit | No fields are changed; existing session and user audit triggers remain authoritative. |
| Presentation | Protobuf timestamp is UTC instant; clients format using their configured timezone. No related IDs are returned. |
| Tests | Unit tests cover active/inactive state, bad IDs, unavailable state store, and non-disclosure of database errors. The Testcontainers integration suite verifies active, mismatched-user, and revoked sessions against migrated PostgreSQL and MySQL. |
| Shared capability | Reuses the configured auth service, gRPC resource/action descriptor and interceptors; no local authentication framework. |
| Dictionary provider | None: sessions are sensitive, ephemeral transactional data and unsuitable for a dictionary. |

The other generated IdentityService methods `RevokeTenantSessions`,
`IssueTenantToken`, and `GetServiceAccount` remain unimplemented. They require
separate domain authorization, audit and token-issuance designs; registering
their transport descriptors does not make their behavior available.
