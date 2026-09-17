# Platform runtime status

## Interface design review

| Concern | Decision |
| --- | --- |
| Contract | `POST /api/v1/platform/runtime/status` has an empty JSON body and returns build identity, process liveness, dependency readiness, and an explicit `ready` boolean in the common response envelope. |
| Authentication | JWT. This is a management-console API; Kubernetes continues to use the public `/live` and `/ready` probe endpoints. |
| Authorization | Platform-scoped `platform.runtime:read`; no tenant or object ownership applies. |
| Operation log | None. Reading transient runtime state is not a business operation. |
| Security log | None. No authentication, credential, session, or privilege state changes. |
| Cache | None. Health and uptime must be sampled at request time. |
| Distributed lock | None. The operation is read-only and has no contention invariant. |
| Optimistic lock | Not applicable because no state is mutated. |
| Audit | Not applicable because no table is written. |
| Presentation | Timestamps remain RFC 3339 values with an explicit offset; dependency names are bounded server-owned identifiers and latency is returned as a Go duration string. |
| Tests | Handler test covers the response envelope and unavailable Redis state; invariant tests cover PBAC registration and Swagger coverage. Frontend tests cover loading, refresh, degraded state, and contract paths. |
| Shared capability | Reuses `health.Service`, `buildinfo.Current`, the shared response envelope, request client, and PBAC route middleware. |

This transient operational resource is not a data-dictionary provider: its
values are live probe results rather than bounded reference data.

