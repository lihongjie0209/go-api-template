# Tenant deletion: application session context cleanup

`application_session_contexts` is tenant-owned session state. A tenant deletion
must retire its active rows in the same database transaction as memberships and
application grants; otherwise the context remains marked active after its
tenant has been deleted. Historical operation/security logs are retained and
are not part of this cascade.

Application lists, context reads, and switches must also join an active,
non-deleted tenant. Membership or application grants alone do not make a
disabled tenant eligible for application access.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Existing platform and tenant delete operations retain their `id` and `version` request and shared response/error codes; no API change. |
| Authentication | Existing JWT route descriptors remain; no public or PSK access is added. |
| Authorization | Existing platform/tenant permission checks run before mutation; cleanup SQL is restricted by the target `tenant_id`. Application list/context read/switch additionally require an active tenant in their SQL authorization predicate. |
| Operation log | Existing `tenant.delete` event records success or failure; no separate context event because this is one domain command. |
| Security log | Existing tenant deletion security event remains; no new credential/session secret is logged. |
| Cache | Existing tenant deletion invalidation remains; database state is authoritative. |
| Distributed lock | None; the tenant row version and transaction are the concurrency controls. |
| Optimistic lock | Existing `WHERE id=? AND version=?` on the tenant root remains; context rows are dependent cleanup, not independent client updates. |
| Audit | The audited context trigger maintains actor, timestamps, version, and soft-delete fields inside the actor-injected transaction. |
| Presentation | No response shape or time format changes. |
| Tests | Unit tests assert scoped context cleanup, rollback on failure, and active-tenant SQL guards for application list/context read/switch. The migration-backed integration case verifies disabled-tenant denial in CI. |
| Shared capability | Reuses `database.Transactor` and audit triggers; no new SDK abstraction. |
| Dictionary provider | Not suitable: application session context is sensitive, transactional state. |
