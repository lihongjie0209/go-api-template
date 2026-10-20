# Permission node action-only targets

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/permissions/update-target` and `/delete-target` accept `{id}`. Update returns editable node fields, parent display name, child-existence hint and current version; delete returns only identity/confirmation fields and version. Existing versioned update/delete remain the only mutations. |
| Authentication | JWT required; no public or PSK exception. |
| Authorization | Target routes bind `permission.definition:update` and `:delete` respectively. No tree/list/read grant is required; the mutation routes repeat authorization. Built-in system nodes are not actionable. |
| Operation/security log | Target reads emit none. Existing mutations emit operation and security events because permission changes affect privileges. |
| Cache | No target cache; read current version. Existing mutation invalidation owns authorization revision refresh. |
| Distributed lock | None; version predicates, tree validation and DB transactions remain authoritative. |
| Optimistic lock | Update/delete submit the target version. A stale version uses the global version-conflict code; child/in-use/system conflicts remain distinct business conflicts. |
| Audit | Read projections omit audit actors/timestamps. Existing transactional actor injection and triggers maintain audit fields. |
| Presentation | Parent name and node type make the target understandable. A child-existence hint keeps the editor from offering a type conversion that cannot succeed; server revalidates on write. |
| Tests | Unit tests cover projections, missing/system nodes, stale versions, action-only UI access, principal/input cancellation and post-commit tree refresh. Testcontainers assertions run later in CI. |
| Shared capability/dictionary | Reuse the Resource/Action interceptor, known-ID lifecycle, tree contract editor and action button. Permission definitions are security configuration, not a dictionary provider. |
