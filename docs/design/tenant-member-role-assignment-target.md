# Tenant member role-assignment target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/tenant-members/roles/target` accepts `{ "membership_id": "UUID" }` and returns `{id,username,display_name,status,version,roles}` in the shared envelope. Each linked role includes mandatory `assignable: boolean`; false means it must be removed before save. It is a bounded edit snapshot; the assignment setter accepts at most 1000 role IDs. Invalid IDs and malformed requests use global invalid-request errors. |
| Authentication | JWT with a current tenant membership; no public, PSK, or wildcard exception. |
| Authorization | `tenant.member:assign-role`, tenant scope, required SQL data permission on the target member. The backend setter repeats all checks and delegation constraints. No `tenant.member:read` or `tenant.role:list` is implied. |
| Operation log | None for this read; the existing `tenant.member.roles.set` mutation records success/failure. |
| Security log | None for this read; the existing role-assignment mutation is the security-relevant privilege change. |
| Cache | None; the target must return current version and links. |
| Distributed lock | None for this read; the setter retains its per-member role-assignment lock. |
| Optimistic lock | The target supplies the current member version. The setter requires that version under tenant, active-status, logical-delete, and data-scope predicates. |
| Audit | Read only. The setter uses the shared transaction actor and audited triggers. |
| Presentation | Return username and display name with the member ID; linked roles include code, name, status, and assignable. Disabled or above-ceiling old links remain visible but cannot be resubmitted. Existing frontend time presentation is unchanged. |
| Tests | Unit-test invalid/foreign/out-of-scope target, linked-role bounds, handler binding, grant-only frontend entry, stale/principal-switch cancellation, and versioned save. PostgreSQL integration is deferred to the feature-complete CI pass. |
| Shared capability | Reuse existing authorization scope, response/error mapping, selection drawer, known-ID panel, and committed-version tracker. No new generic infrastructure is needed. |
| Dictionary provider | No; member-role assignment is sensitive transactional data, not a public bounded dictionary. |

| Route | Authentication | Resource/action | Scope | Data filtering |
| --- | --- | --- | --- | --- |
| `/tenant-members/roles/target` | JWT | `tenant.member:assign-role` | Current tenant and member object | Required `tenant.member` SQL scope |

This endpoint permits an operator who can assign roles to a known member to
open the existing editor without unrelated read/list grants. It is not an
authorization grant in itself. The frontend may show manual role IDs when role
catalog browsing is unavailable; the setter remains authoritative and rejects
roles outside the actor's delegation ceiling.
