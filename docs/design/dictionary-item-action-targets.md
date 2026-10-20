# Dictionary item action-only targets

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/dictionary-items/update-target` and `/delete-target` accept `{id}`. Update returns the item identity, owning dictionary ID, editable fields, parent display name and version; delete returns only confirmation identity, dictionary ID and version. Existing POST mutations remain authoritative. |
| Authentication | JWT; neither route is public or PSK. |
| Authorization | Bind targets to `dictionary.item:update` and `dictionary.item:delete` respectively. No read/list grant is required. The frontend verifies returned `dictionary_id` matches its current route; the server repeats action authorization on mutation. |
| Operation/security log | Lookups emit neither. Existing mutations emit operation logs; no security event is needed for non-privilege dictionary content. |
| Cache | Targets are uncached so versions stay fresh. Invalidate the owning dictionary's public cache only after a confirmed mutation. |
| Distributed lock | None for the target reads. Existing item writes serialize on the definition row and retain transaction/version constraints. |
| Optimistic lock | Update/delete use the target's version and report stale versions with the shared global error; no automatic retry. |
| Audit | Read projections omit audit actors/timestamps. Existing transaction actor injection and triggers maintain mandatory fields on writes. |
| Presentation | Item code/name and parent name make the target understandable without exposing unrelated audit fields. |
| Tests | Unit tests cover narrow projections, invalid/missing targets, action-only capability flow, scope mismatch, stale versions, and post-commit cache behavior. Testcontainers lifecycle assertions run later in CI. |
| Shared capability/dictionary | Reuse common action interceptors, known-ID target lifecycle, editor, action button and dictionary cache. Item records are dictionary content rather than an additional provider. |
