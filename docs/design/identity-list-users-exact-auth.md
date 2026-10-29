# Identity ListUsers exact lookup authentication

The `ListUsers` gRPC adapter supports a documented `=username` exact mode
for tenant membership eligibility checks. The mode uses the authoritative
Identity repository so stale profile caches cannot make a disabled or deleted
identity eligible.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Exact mode returns zero or one user with matching page metadata. Invalid usernames return `InvalidArgument`; missing caller returns `Unauthenticated`. Canceled requests and exceeded deadlines retain their gRPC codes across all user-query modes. Normal pagination remains unchanged. |
| Authentication | Service-only gRPC descriptor, with an additional handler entry check before either exact or paginated dispatch. The login module's separate anonymous authoritative lookup is unchanged. |
| Authorization | Existing `identity.user:list` platform resource/action interceptor remains authoritative. The handler check does not replace it. |
| Operation log | None: bounded read-only lookup. |
| Security log | None: this RPC does not authenticate the target user or change security state. |
| Cache | None for exact eligibility lookup; repository status and logical-delete state remain authoritative. |
| Distributed lock | None: read-only. |
| Optimistic lock | Not applicable. |
| Audit | No mutation. |
| Presentation | Existing user model includes display name, status, and RFC 3339-compatible protobuf timestamps. |
| Tests | Unit regression requires a principal before exact lookup and verifies no SQL is issued without one; existing exact active/disabled/deleted/cache-staleness cases remain. |
| Shared capability | Reuses the shared principal and resource/action enforcer; no local authorization framework. |
| Dictionary provider | None: user identity/contact data is sensitive and transactional. |
