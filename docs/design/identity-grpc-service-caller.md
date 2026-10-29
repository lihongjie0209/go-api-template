# Identity gRPC service caller boundary

`GetUser`, `BatchGetUsers`, and `ListUsers` are platform-scoped internal reads.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Existing protobuf requests and responses remain unchanged; missing caller returns `Unauthenticated`, non-service caller `PermissionDenied`. |
| Authentication | Only service-account principals are accepted at both interceptor and handler boundaries. |
| Authorization | Existing `identity.user:read/list` resource/action descriptors remain authoritative; the handler guard is defense in depth, not a substitute for grants. |
| Operation/security log | None: bounded read-only calls without authentication or mutation. |
| Cache | Existing Identity profile cache retains authoritative version/deletion check. |
| Distributed/optimistic lock and audit | Not applicable to reads. |
| Presentation | Existing protobuf user model carries display name, status and timestamps. |
| Tests | Unit tests cover missing and wrong-type principals before repository access, plus existing read, batch and exact-search cases. |
| Shared capability | Reuse shared principal type and existing authorization interceptor; no new middleware. |
| Dictionary provider | None: user identity/contact data is sensitive and transactional. |
