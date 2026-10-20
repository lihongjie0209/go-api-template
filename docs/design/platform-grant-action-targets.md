# Platform grant action targets

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/platform/permission-grants/update-target` and `/delete-target` accept `{id}` (UUID) and return only the fields needed to edit or confirm revocation, including the current `version`. Invalid IDs use the global invalid-request code; missing or deleted grants use not-found. |
| Authentication | Both are JWT-only. No wildcard/PSK exception. |
| Authorization | Platform-scoped `authorization.platform-grant:update` protects the update target; `authorization.platform-grant:delete` protects the delete target. Neither requires list/read. No tenant/data-scope bypass is introduced. |
| Operation log | Lookup only, so none. Existing mutations record `authorization.platform-grant.update/delete` with resource ID and result, without credentials. |
| Security log | Lookup only, so none. Existing mutations record permission-change security events and fail closed if enqueue fails. |
| Cache | No cache: grant version and identity must be authoritative immediately before mutation. |
| Distributed lock | No new lock; existing serializable transaction, row lock and version predicate protect mutations. |
| Optimistic lock | Update/delete submit the target version; stale versions are rejected, never automatically retried. |
| Audit | Read-only targets do not change audit fields. Existing mutation transaction supplies the actor to audit triggers. |
| Presentation | Include a human-readable principal and permission label for confirmation. Exclude audit actors/timestamps; delete target omits editable foreign-key IDs. UI shows a clear stale/uncertain-result state. |
| Tests | Unit tests cover exact projection, malformed/missing IDs, dependency errors, capability-specific lookup, target changes, stale versions, and post-commit refresh. Testcontainers coverage runs later in CI, not during ordinary local development. |
| Shared capability | Reuse the existing Resource/Action interceptor, response/error envelope, known-target composable, editor, authorized action button and authorization-revision refresh. Grants are sensitive transactional data and are not a dictionary provider. |
