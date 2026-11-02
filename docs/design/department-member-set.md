# Department member set reconciliation

`POST /api/v1/tenant-departments/members/set` replaces one department's member
set. The request keeps the department `id`, expected `version`, and at most
1,000 distinct `{membership_id,is_primary}` assignments. The response keeps the
shared envelope.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Empty assignments remove every current link. Invalid, duplicate, or inactive member IDs fail before association writes; stale department version returns the global stale-version error. |
| Authentication | JWT tenant principal through the registered route descriptor. |
| Authorization | `tenant.department:assign-member` and the current-row data scope guard the department; every link read/write includes the authenticated tenant ID. |
| Operation log | Record `tenant.department.members.set` with bounded counts and department ID, atomically with the mutation. |
| Security log | Record membership change in the same outbox transaction; a failed enqueue rolls back. |
| Cache | None; department membership is transactional and every read uses SQL. |
| Distributed lock | Reuse the tenant `members` lock, which serializes primary-department changes within one tenant. Active membership rows are locked before association rows; membership removal now writes its member row before links in the same order. SQL constraints remain authoritative. |
| Optimistic lock | Claim the department version first. Every changed/deleted/restored link also uses its selected version and checks one affected row. Unchanged links keep their versions. |
| Audit | The authenticated user is the SQL actor. Triggers maintain created/updated/deleted fields and versions on changed links. |
| Presentation | Existing member-list API supplies user names; this command returns only the shared success envelope. |
| Tests | Unit cases cover no-op, active-member row locks, removal, primary promotion, latest historical restoration, bounded sets, and stale link writes. PostgreSQL/MySQL Testcontainers assert unchanged link versions across repeated sets and restoration of a soft-deleted link. |
| Shared capability | Reuse the transaction, lock, operation/security recorders, data scope, and pagination bounds; no new infrastructure implementation. |

`tenant_department_members` is an association and is unsuitable as a public
data-dictionary provider. The set query reads at most 1,001 current links and
rejects a pre-existing set over the 1,000-link contract. Historical restoration
selects only deleted rows with a deterministic newest-first order; MySQL's
full-key uniqueness and PostgreSQL/Kingbase's active-only uniqueness both work.
