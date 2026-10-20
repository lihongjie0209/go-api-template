# Data-dictionary definition action targets

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/dictionaries/update-target` and `/delete-target` accept `{id}`. Update returns editable fields, immutable code/type/source and version; delete returns only confirmation identity and version. Both use the shared response envelope and global errors. |
| Authentication | JWT required; no public or PSK bypass. |
| Authorization | Target routes bind `dictionary.definition:update` or `:delete` respectively. Neither needs read/list. The definition is platform-owned and does not inherit an item permission. |
| Operation/security log | Lookups emit neither. Existing mutations emit operation logs; no security log because they do not change credentials, membership or privileges. |
| Cache | Targets are never cached because version is authoritative. Successful writes invalidate the public dictionary cache by immutable code after commit. |
| Distributed lock | None. Existing version predicates and transaction protect writes; deleting a non-empty dictionary remains a business conflict. |
| Optimistic lock | Update/delete carry the freshly read version. A mismatched version maps to the global stale-version code; non-empty delete remains a distinct conflict. No automatic retry. |
| Audit | Lookup is read-only and omits audit actors/timestamps. Existing transactions inject actor context and triggers maintain audit columns. |
| Presentation | Return code, name and type/source for understandable confirmation, not opaque IDs. The current Vben form is reused rather than rebuilt. |
| Tests | Unit tests cover projection, invalid/not-found, version conflict, isolated action-only visibility, current-target lifecycle and cache invalidation. Testcontainers lifecycle assertions are written but run in CI only. |
| Shared capability/dictionary | Reuse the common Resource/Action interceptor, target composable, editor, action button and cache module. Definitions are metadata *about* dictionaries, not a dictionary provider themselves. |
