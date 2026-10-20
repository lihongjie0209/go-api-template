# Resource/Action catalog action targets

| Concern | Decision and rationale |
| --- | --- |
| Contract | Four POST+JSON endpoints accept `{id}`: `/authorization/resources/{update,delete}-target` and `/authorization/actions/{update,delete}-target`. Update targets contain editable fields, immutable keys/parent and version; delete targets contain a display identity and version. Responses use the shared envelope and global errors. |
| Authentication | JWT required; no PSK or anonymous route. |
| Authorization | Each endpoint binds its own `authorization.resource` or `authorization.action` `update`/`delete` action. List/read is not required. Target lookup rejects code-owned rows so only manual definitions can be mutated. Platform scope; no tenant or row-data bypass. |
| Operation/security logs | Lookups emit neither. Existing update/delete transactions record operation and security changes without credentials. |
| Cache | No target cache: version and manual ownership must come from the authoritative database. Successful mutations use the existing authorization revision refresh. |
| Distributed lock | None. Existing serializable transaction, row lock, uniqueness constraints, and version predicate protect mutations. |
| Optimistic lock | All four mutations submit the loaded version. A stale version uses the global stale-version code; uniqueness/in-use/code-owned conflicts retain their distinct conflict semantics. No client retry of a mutation. |
| Audit | Lookups are read-only; mutation transactions inject the actor and triggers maintain audit fields. |
| Presentation | Include resource/action display names and stable keys for confirmations. Hide audit actor IDs and timestamps in action-target responses. Existing Vben table/editor components remain the visual system. |
| Tests | Unit tests cover manual/code-owned targets, invalid/missing IDs, exact response projection, version conflict, action-only page gating and post-commit behavior. Testcontainers remains a later CI-only gate. |
| Shared capability/dictionary | Reuse the shared Resource/Action interceptor, error envelope, known-target composable, editor, authorized action button, and authorization revision. Authorization definitions are sensitive transactional data, not dictionary providers. |
