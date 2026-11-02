# Authentication dependency error boundary

User-session and service-account authentication must distinguish invalid
credentials from an unavailable security outbox. The public error remains the
global dependency-unavailable code; raw outbox errors stay inside the recorder
and must not be embedded in a returned error string. A tenant membership's
identity lookup likewise treats a failed or missing gRPC response as an
identity dependency failure. Caller cancellation and deadlines preserve their
context error classes so HTTP/gRPC timeouts are reported correctly.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Existing login, service-account authentication and tenant-member add contracts are unchanged; dependency failures map to the existing global unavailable code. |
| Authentication | These paths retain JWT/session or credential checks before protected actions; login itself can run without a principal. |
| Authorization | Existing Resource/Action descriptors and tenant-scoped SQL checks remain in force. |
| Operation log | Existing mutation failure event uses a fixed safe code; no raw dependency error is added. |
| Security log | Successful token/session actions fail closed when the transactional event cannot commit. Rejected attempts retain the existing bounded failure event. |
| Cache | No authentication decision is made from a profile cache; identity eligibility remains authoritative. |
| Distributed lock | Existing per-account/login and per-member locks are unchanged; errors do not affect lock scope. |
| Optimistic lock | Credential/session and membership writes keep their version or credential-state predicates. |
| Audit | Existing transaction and audit triggers remain the only mutation path. |
| Presentation | Public errors remain generic and include normal correlation IDs; no upstream connection text or secret appears. |
| Tests | Unit regressions inject secret-bearing dependency errors and nil identity responses, assert safe classifications, and verify caller cancellation/deadlines. Integration tests remain CI-only. |
| Shared capability | Reuses the global error taxonomy, transactional security recorder and Context propagation. |
