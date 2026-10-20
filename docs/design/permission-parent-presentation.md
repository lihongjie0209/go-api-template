# Permission parent presentation

| Concern | Decision and rationale |
| --- | --- |
| Contract | Existing permission get/create/update/tree responses add `parent_name`; request models, error codes, and tree filters do not change. |
| Authentication | Keep existing JWT-protected permission routes. |
| Authorization | Keep canonical `permission.definition` actions; get resolves only a non-deleted parent, while tree derives names from its returned nodes. No new privilege is introduced. |
| Operation log | Read operations emit none; existing mutation events remain unchanged. |
| Security log | None; this is presentation metadata, not a security transition. |
| Cache | None; permission definitions are read from the authoritative repository here. |
| Distributed lock | None for presentation reads; writes retain existing constraints. |
| Optimistic lock | Update/delete version predicates remain unchanged. |
| Audit | No write path or actor-source change. |
| Presentation | The UI shows the parent name, “根节点” for roots, or “父权限不可用” if a referenced parent is unavailable; it never displays an opaque parent ID as the label. |
| Tests | Go unit tests cover direct lookup and bounded tree name derivation; frontend unit tests cover all three display states. Existing Testcontainers lifecycle test adds assertions but remains CI-only. |
| Shared capability | Reuse the current tree and resource-detail components; no SDK extraction is needed. |
| Dictionary provider | No provider: permission definitions are security configuration rather than broadly selectable dictionary data. |
