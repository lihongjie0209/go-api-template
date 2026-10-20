# Navigation action-only targets

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/navigations/update-target` and `/delete-target` accept `{id}`. Update returns editable navigation fields, immutable application/key identity, application/parent display names and version; delete returns narrow display identity and version. Both use the shared envelope and global error codes. |
| Authentication | JWT. No public, PSK, or internal-only bypass. |
| Authorization | Update target binds `navigation:update`; delete target binds `navigation:delete`. Neither requires `navigation:read/list` or application read. These are platform-owned navigation definitions, with no tenant row scope. Backend mutations recheck their own action. |
| Operation/security log | Lookups emit neither. Existing update/delete transaction operation logs remain authoritative. No security log: no credential, membership, or privilege grant changes. |
| Cache | No target cache; versions and parent identity come from authoritative SQL. A committed mutation invalidates the application menu cache and refreshes current routes. |
| Distributed lock | None. The existing serializable transaction, tree validation, and version predicate protect mutations. |
| Optimistic lock | Update/delete submit the target version. Missing version match maps to global stale-version error; a non-leaf deletion and invalid tree remain business conflicts. No automatic mutation retry. |
| Audit | Targets are read-only and omit audit identities; existing mutation transactions inject the actor and database triggers maintain all audit columns. |
| Presentation | Target includes application name and parent name instead of showing only related IDs. The existing Vben editor and known-ID panels are reused. Timestamps are absent from targets. |
| Tests | Unit tests cover target projection, deleted/missing IDs, action-only route metadata, stale version mapping, known-ID target lifecycle, editor/confirmation, and post-commit menu synchronization. Testcontainers remains CI-only. |
| Shared capability/dictionary | Reuse the shared Resource/Action interceptor, envelope, known-target composable, editor, action button, and route refresh. Navigation definitions are transactional/security-sensitive, not data-dictionary providers. |
