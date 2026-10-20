# Platform configuration action targets

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/platform-configs/update-target` and `/delete-target` accept `{id}`. The update response returns immutable key, editable fields, value type, and version; delete returns identity, public flag, status, and version. Both use the shared envelope and errors. |
| Authentication/authorization | JWT. Each target binds to its matching `platform.config:update` or `platform.config:delete` action. No list/read grant is required. Platform scope has no tenant/data predicate. |
| Operation/security logs | Lookups emit neither. Existing update/delete transactions emit operation and platform-configuration security events; values never enter those logs. |
| Cache/lock | Targets read the authoritative database. Existing mutation uses its narrow config lock and invalidates public cache after commit. No new cache or lock is introduced. |
| Optimistic lock/audit | Mutations require the target version. Zero affected rows return the global stale-version code, separate from the create-key conflict. Transaction actor and audit triggers remain authoritative. |
| Presentation/security | Target response excludes audit actors and timestamps. Update reveals a configuration value only to an operator with update permission; delete does not reveal the value. The UI warns explicitly when `is_public` is enabled. |
| Tests | Unit tests cover projection, invalid/missing/deleted records, database failure, stale versions, frontend capability gating, changed target, and post-commit refresh. The existing Testcontainers lifecycle adds target assertions for the next CI run; it is not run locally. |
| Shared capability/dictionary | Reuse known-action target, shared editor, authorized button, response/error contracts, and public-branding refresh. Operational configuration is not exposed as a dictionary. |
