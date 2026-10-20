# Tenant member known-ID action targets

The member page must let an operator holding only `tenant.member:update` or
`tenant.member:remove` act on a known membership ID. Neither action grants
member `read` or `list`.

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/tenant-members/status/target` and `/tenant-members/remove-target` accept `{ "id": "uuid" }`. Both return only `{id,username,display_name,status,version,is_self}`. `is_self` is computed from the authenticated principal to let the UI leave a disabled/removed tenant. Missing/malformed IDs are invalid requests; missing or out-of-scope rows are not-found. No pagination, search, or sort applies to a single target. |
| Authentication | JWT with an active tenant membership. |
| Authorization | Status target binds `tenant.member:update`; removal target binds `tenant.member:remove`. Both compile the same action-specific SQL data scope as the command and constrain `tenant_id` in the repository. The command rechecks scope and version. The target never grants member `read` or `list`. |
| Operation log | None for target reads. Existing status/removal commands record mutation success or failure. |
| Security log | None for target reads. Existing membership commands emit security events. |
| Cache | None; the target version and status must be current immediately before confirmation. |
| Distributed lock | None for reads. The write uses its existing transaction, tenant-admin invariant check, and optimistic version. |
| Optimistic lock | Both commands submit the target version. A stale-version error clears the target and requires another lookup; no automatic write retry. |
| Audit | Read-only targets. Existing writes continue through the transaction-local actor and audit trigger. |
| Presentation | The target includes username and display name for human confirmation; no other IDs, audit actors, contact data, or role grants are returned. |
| Tests | Unit tests for projection, tenant/data-scope query, missing/invalid IDs, action-only page entry, stale lookup/authorization/principal cancellation, conflict, post-commit handling, and self-membership exit. Testcontainers tenant-isolation and command lifecycle remain CI integration work. |
| Shared capability | Reuse the existing scoped repository lookup, Resource/Action descriptor, response/error envelope, `GoReadByIDPanel`, `useKnownActionTarget`, and authorized action button. No new SDK abstraction. |

Memberships are transactional and security-sensitive, so they are not a
data-dictionary provider.
