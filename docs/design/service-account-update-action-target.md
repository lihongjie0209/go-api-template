# Service-account update-only target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/service-accounts/update-target` takes `{id}` and returns `{id,client_id,name,description,status,expires_at,version}`. The client ID is immutable; the other fields are editable. Shared invalid/not-found errors and response envelope apply. No pagination. |
| Authentication/authorization | JWT and canonical `identity.service-account:update`; no `read` or `list` grant is required. The route registry fails closed when undeclared. |
| Operation/security log | Lookup emits neither. The existing update operation is logged; no credential is changed, so this lookup has no security event. |
| Cache/lock | Fresh owner database read supplies current version. No cache or distributed lock; the existing `WHERE id AND version` update is the contention guard. |
| Optimistic lock/audit | Submit must include returned version. Existing transaction and audit actor rules remain unchanged. |
| Presentation | Return only fields needed by the editor, never secret, secret hash, failed-auth counters, lock state, actor identifiers, or audit fields. `expires_at` is RFC3339 with offset when present. |
| Tests | Handler unit tests assert exact projection, invalid/missing IDs and dependency failures. The existing local-service Testcontainers lifecycle covers `Get` and versioned `Update` when CI runs; the new handler projection is covered by its unit test. Frontend API/page and real-capability tests check update-only access and stale target invalidation. |
| Shared capability/dictionary | Reuse route authorization, response envelope, known-ID target composable and resource editor. Service accounts are sensitive transactional identities, not a public dictionary. |
