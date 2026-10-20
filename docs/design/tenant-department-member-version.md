# Department membership replacement concurrency

`POST /api/v1/tenant-departments/members/set` replaces the selected department's
membership set. The client must send the department's current `version`; the
service claims that version in the same transaction as the association writes.
A stale editor receives the shared version-conflict error and must reload.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Keep POST+JSON and the response envelope. Require `department_id`, positive `version`, and at most 1000 unique `members`; reject malformed input and stale versions. |
| Authentication | JWT with an active tenant and membership; this is not a public operation. |
| Authorization | `tenant.department:assign-member`, with the existing tenant and department data scope checked before any write. |
| Operation log | Record `tenant.department.members.set`, department ID/name, assignment count, success/failure; never log the full member list. |
| Security log | Record `membership_changed` in the same transaction through the existing security-log outbox. The event contains the department and assignment count, never the full member list. A failed enqueue rolls back the write. |
| Cache | No response cache. The existing authorization-change signal refreshes capability/navigation state after commit. |
| Distributed lock | Reuse the existing tenant-scoped department-membership lock because changing a primary member can touch another department. Database version and constraints remain authoritative. |
| Optimistic lock | Claim `WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NULL` before replacing links; zero affected rows is a version conflict. |
| Audit | Existing transaction actor context and PostgreSQL/Kingbase audit trigger update the department and association audit fields. |
| Presentation | Drawer closes after success and reloads the department tree to obtain the new version; synchronization failure is not reported as failed save. |
| Tests | Unit tests pin required version, stale-version rejection before link writes, successful version claim, and frontend request/reload behavior. Existing integration coverage is updated but run in CI only. |
| Shared capability | Reuse the existing resource version-conflict handling, transaction, lock, authorization, and tree components. Department membership is transactional/sensitive, so it is not a dictionary provider. |
