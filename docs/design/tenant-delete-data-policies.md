# Tenant deletion: data-permission policy lifecycle

Tenant-scoped data-permission policies belong to the tenant even though their
version and action tables do not repeat `tenant_id`. Deleting a tenant retires
all three levels in the same actor-injected transaction. Global policies and
other tenants' policies remain untouched. The existing database revision poll
recovers policy snapshots; a successful delete also requests an immediate
local refresh and Redis invalidation hint.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Existing tenant delete POST operations keep `id`, `version`, response, and shared error codes. |
| Authentication | Existing JWT descriptors remain; no new public or PSK route. |
| Authorization | Existing platform/tenant permission check precedes deletion. Child-policy updates derive scope only through a parameterized parent-policy `scope='tenant' AND tenant_id=?` predicate. |
| Operation log | Existing tenant-delete operation event covers the whole transaction. |
| Security log | Existing tenant-delete security event remains; policy documents are not logged. |
| Cache | Commit precedes policy runtime refresh and Redis invalidation. Refresh detaches from client cancellation but has a bounded deadline; database revision polling recovers lost hints. |
| Distributed lock | None; tenant version and one database transaction serialize the domain deletion. |
| Optimistic lock | Tenant root retains `WHERE id=? AND version=?`; dependent rows are cascade cleanup, not independent client updates. |
| Audit | Audit triggers maintain actor, timestamps, soft-delete fields and versions for policy, version and action rows. |
| Presentation | No response shape or timestamp format changes. |
| Tests | Unit tests assert tenant-scoped cleanup order, rollback on failure, post-commit refresh and canceled-client recovery; integration test checks global/other-tenant preservation. |
| Shared capability | Reuses transaction/audit and the existing data-permission runtime sync; no parallel cache mechanism. |
| Dictionary provider | Policies are sensitive authorization state, not dictionary data. |
