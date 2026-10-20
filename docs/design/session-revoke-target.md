# Known-ID session revocation target

## `POST /api/v1/auth/sessions/revoke-target`

| Concern | Decision |
| --- | --- |
| Contract | Accept `{session_id}` and return only `{id,client_ip,user_agent,status,expires_at,version}`. Reject empty/oversized IDs; unknown and other users' IDs share the not-found response. |
| Authentication | JWT user principal only. |
| Authorization | `identity.session:revoke`, independently of `identity.session:list`; SQL requires `user_id = current user_id` and `deleted_at IS NULL`. No tenant membership can expand this identity-owned scope. |
| Operation log | None for target lookup; the existing revoke command records the security event. |
| Security log | None for target lookup; the existing revoke command emits `session.revoked`. |
| Cache | None; the version and state must be read from the authoritative database. |
| Distributed lock | None; the existing versioned revoke mutation uses the database predicate. |
| Optimistic lock | Target supplies the current version; revoke still requires `{session_id,version}`. |
| Audit | Read-only. Existing revoke path maintains audit fields. |
| Presentation | Show IP, user agent, expiry, and current-device marker before confirmation; timestamps have explicit offset. Never include refresh-token hashes. |
| Tests | User scope, missing/oversized ID, not found, active/revoked status, redacted response, frontend stale lookup and principal switch, versioned revoke. |
| Shared capability | Reuse Resource/Action interceptor, response envelope, known-ID panel, and authorized action button. |
| Dictionary provider | Not suitable: sessions are sensitive transactional records. |

The UI keeps the existing list workflow for users with `:list`. A user with
`:revoke` alone may enter a known session ID, review this narrow target, and
confirm revocation. The backend remains authoritative at the command boundary.
