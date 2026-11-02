# Tenant context response time

The `available`, `switch`, and `current` tenant-context operations return a
membership `joined_at` instant. Convert it to the shared +08 presentation
timezone only after database reads, authorization and token signing. The
persisted instant and SQL predicates remain timezone-aware and unchanged.
Rejected switches omit malformed or overlong target IDs from the security
event, so invalid input cannot turn a normal denial into an audit validation
failure. An audit outage remains fail-closed with a fixed public reason.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Existing POST+JSON tenant-context requests and response models stay the same; `joined_at` is rendered with `+08:00`. |
| Authentication | JWT user principal for all three operations; `switch` also requires a live session. |
| Authorization | Existing Resource/Action descriptors and SQL membership/active-tenant checks remain the authority. |
| Operation log | No new event for these reads; switch already has a security event. |
| Security log | Switch success and failure continue through the existing fail-closed security recorder. Malformed target IDs are omitted and raw audit dependency errors do not enter the returned error. |
| Cache | None; active membership and tenant status are read from SQL for each operation. |
| Distributed lock | None; no shared state is mutated by the presentation change. |
| Optimistic lock | Not applicable to read/selection; membership mutations keep their existing version predicates. |
| Audit | The database owns `joined_at` and mandatory audit fields; response conversion is copy-only. |
| Presentation | Shared `presentation.Time` converts the response instant to +08 without changing its meaning. |
| Tests | Unit tests exercise all three response paths with a UTC database instant and assert the +08 JSON-facing value, bounded rejected targets and safe audit-outage errors. |
| Shared capability | Reuses the shared presentation helper; tenant context remains an owned transactional API, not a public dictionary provider. |
