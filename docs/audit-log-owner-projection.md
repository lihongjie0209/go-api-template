# Audit log owner presentation

The tenant-scoped operation/security log `get` and `page` APIs now return
`tenant_name`; operation logs also return `application_name`. These are the
**current names** from this monolith's tenant/application tables. The stable
IDs remain in the response and the UI labels the names accordingly. Existing
actor, subject, and resource names continue to use historical snapshots.
Missing owner records return an empty name, which the shared reference
component presents as unavailable without inventing one. Soft-deleted owners
retain their last stored name for investigation.
The operation-log list displays the application name directly so investigators
can distinguish records without opening each detail panel; the tenant name is
shown in detail because tenant log routes are already scoped to one tenant.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Keep existing POST `get`/`page` inputs, sort/filter limits, envelope and error codes; add string display fields to log records. No new endpoint. |
| Authentication | Existing JWT descriptors remain; system log routes still require platform context, tenant log routes still require active tenant. |
| Authorization | Existing operation/security log read/list Resource/Action checks and SQL tenant scope remain authoritative. Name subqueries use only the scoped record's IDs. |
| Operation log | Keep the existing audited operation-log reads; no new write event for display enrichment. |
| Security log | Keep the existing security-log access event policy; display enrichment creates no new event. |
| Cache | None: bounded page/get queries read current owner names from local indexed primary keys. |
| Distributed lock | None: read-only projection has no contention invariant. |
| Optimistic lock | Not applicable to read-only log APIs. |
| Audit | No log table mutation; existing actor/timestamp/version triggers remain unchanged. |
| Presentation | Show current tenant/application name with the stable ID available as a tooltip. Historical actor/subject/resource snapshots are not overwritten. Times stay RFC 3339 with Asia/Shanghai display. |
| Tests | Go unit tests pin scoped SQL and scan the name projections; frontend unit tests pin reference-field mappings. Database compatibility and actual names will be checked in the next substantial CI integration run. |
| Shared capability | Reuse the shared `GoEntityReference`, audit page contracts, and existing backend presentation flow; no new component. |
| Dictionary | Audit logs are sensitive, append-only, and unbounded; they are not data-dictionary providers. |

This is a current-name projection, not a historical owner-name snapshot. If
product requirements need the tenant/application names as they were at the
exact event time, add versioned event-payload snapshots and a migration for
the partitioned log tables rather than relabeling this projection.
