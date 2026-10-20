# User logout contract

`POST /api/v1/auth/logout` revokes the one session identified by the submitted refresh token. The token itself is the credential for this exact endpoint, so a valid access JWT is not required. This lets a user revoke a session after the access JWT expires without rotating the refresh token merely to log out. Other session-management endpoints remain JWT-protected.

| Concern | Decision and rationale |
| --- | --- |
| Contract | POST JSON `{"refresh_token":"..."}`; success uses the common envelope. Missing, invalid, or already-revoked tokens fail with global validation/authentication errors. |
| Authentication | Public route descriptor; the unguessable refresh token is verified against its stored hash inside `authentication.Service.Logout`. No wildcard or PSK bypass is involved. |
| Authorization | Possession of this refresh token permits revocation of that token's session only. It cannot enumerate or revoke another session by ID. |
| Operation log | No general operation log; this credential action uses the independent security log. |
| Security log | Successful and failed logout attempts record a security event; raw refresh tokens are converted to keyed digests, not stored in logs. Security-event enqueue remains fail-closed for a successful revocation. |
| Cache | No response cache. Session revocation is authoritative in PostgreSQL; normal session validation observes it. |
| Distributed lock | None; row locking and the conditional database update serialize revocation of one session. |
| Optimistic lock | The service selects the session `FOR UPDATE` and updates it with the expected row version. A repeated revoke is rejected. |
| Audit | The transaction uses an explicit `identity-service:logout` system actor; audit triggers maintain mandatory fields. |
| Presentation | The response contains no token or session data. The UI clears local credentials even when the revocation request fails. |
| Tests | Unit tests cover credential-client selection, local cleanup, hashed-token revocation, and duplicate/invalid token behavior; normal integration gates remain in CI. |
| Shared capability | Reuses the shared response, error, security-log, transaction, and authentication middleware facilities. |

The session record is security-sensitive transactional data and is not exposed as a data-dictionary provider. The rate limiter and common request timeout still apply to this public route.
