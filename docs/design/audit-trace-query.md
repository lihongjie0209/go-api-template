# Audit trace lookup

The tenant operation/security log pages and platform system operation/security log pages accept optional `trace_ids` in their existing POST page request. This is an exact-match diagnostic filter, not a new authorization path.

| Concern | Decision and rationale |
| --- | --- |
| Contract | `trace_ids` is an optional list of 1–200 nonblank values, each at most 64 bytes (matching the persisted trace field). An empty list means no filter. Values combine with other filters by `AND`; count and items use identical predicates and the existing stable occurrence-time sort. Existing global invalid-input errors apply. |
| Authentication and authorization | Existing JWT and canonical operation permissions remain. Tenant pages still require the principal tenant predicate; platform pages still query only system records and require their explicit 31-day time window. A trace ID never grants access outside those scopes. |
| Operation/security log | Query-only operation-log access keeps its existing aggregate audit record; it records the number of trace filters, never their values. Security-log access continues to record the existing independent security event. |
| Cache and distributed lock | Neither: authoritative diagnostics are read from log persistence and no write invariant exists. |
| Optimistic lock and audit | Read-only; existing record audit fields and versions are unchanged. |
| Presentation | The existing copyable Trace ID detail field is paired with an advanced exact-match filter on both pages. Existing Shanghai timestamp rendering and related display names are unchanged. |
| Tests | Unit tests cover bounded input, tenant-first parameterized count/list SQL, HTTP request mapping, and frontend query normalization. Database/index behavior belongs to the later CI integration and operational plan review. |
| Shared capability and dictionary | Reuse existing pagination, query form, authorization and response components. Logs are sensitive transactional records and are not dictionary-provider data. |

The log tables are large and partitioned. The tenant/occurrence indexes and platform 31-day maximum remain in force; an additional trace-specific index needs an operational plan review before migration because write amplification and partition count are material.
