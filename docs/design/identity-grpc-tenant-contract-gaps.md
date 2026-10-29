# Identity gRPC tenant-operation contract gaps

The shared `IdentityService` protobuf currently advertises two RPCs that the
template intentionally leaves unimplemented. Their present request messages do
not provide enough authority or scope to implement them safely. Transport
authorization metadata alone does not repair the missing domain contract.

| RPC | Current gap | Required contract before implementation |
| --- | --- | --- |
| `IssueTenantToken(user_id, tenant_id, membership_id)` | A user access token requires an authoritative live `session_id`. The request has none. Signing with an invented session, a service caller's session, or an arbitrary user session would enable impersonation or produce an invalid token. | Require an explicit target session ID; verify it belongs to the target user, is unrevoked/unexpired, and that the tenant and membership are active and match. Define caller permission, purpose, idempotency exclusion, short expiry, and a mandatory security event that commits before the token is returned. Prefer an operation bound to the authenticated user's own session unless an explicitly audited delegation use case is approved. |
| `RevokeTenantSessions(user_id, tenant_id, reason)` | `identity_sessions` has no tenant ID. A session may carry multiple tenant/application contexts. Revoking whole user sessions selected from one tenant context would also invalidate access to other tenants. Deleting an application context alone would not revoke JWTs because current token-state verification does not consult that table. | Define whether the command revokes tenant-context tokens only or whole sessions. For tenant-only revocation, persist a tenant/session revocation or generation boundary checked by every HTTP/gRPC user-token verification; make the change, versioned session/context update, security event, and outbox fact atomic. For whole-session revocation, name the broader side effect explicitly and require platform-level authority. Bound the reason and return an unambiguous count. |

Neither RPC may be made functional by selecting a random live session, by
revoking all sessions for a user, or by treating a tenant-context row as a
revocation signal without changing token verification. The protobuf contract
belongs in the shared `platform-protos` repository and must be versioned there
before this template implements either operation. Until then, generated gRPC
`Unimplemented` responses are preferable to unsafe partial behavior.

The safe existing paths are user-initiated tenant context switching and
explicit all-session logout. These remain distinct operations with their own
authorization and security logging.
