# Dictionary parent candidates

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/dictionary-items/page` accepts optional `exclude_subtree_id` (UUID). When set, both `total` and `items` exclude that active item and every active descendant in the same dictionary. Existing page/search/parent/disabled filters combine with AND; sorting is `sort_order,code,id`. |
| Authentication | Existing JWT requirement remains. |
| Authorization | Existing `dictionary.item:list` gate remains; the recursive search is anchored to `dictionary_id` and excludes deleted rows. This is a candidate hint, not permission to mutate. |
| Operation log | None for read-only candidate lookup. |
| Security log | None; no security state changes. |
| Cache | None; candidate queries use authoritative database state so moves appear immediately. |
| Distributed lock | None; the final update still validates ancestry transactionally. |
| Optimistic lock | No mutation here; dictionary update keeps its expected-version predicate. |
| Audit | No writes or actor-field changes. |
| Presentation | Existing parent names and item labels remain. The frontend sends the edited item ID so the picker omits self/descendants instead of waiting for a save error. |
| Tests | SQL-mock unit tests cover UUID validation and count/page filter parity; frontend unit test covers the request; PostgreSQL/MySQL Testcontainers lifecycle assertions cover actual subtree exclusion in CI. |
| Shared capability | Uses the existing page contract, dictionary API client and editor select; no new SDK component. |
| Dictionary provider | Not applicable: this is management of static items, not a provider registration. |

The recursive CTE uses `UNION` rather than `UNION ALL` to terminate on a
corrupted cycle. PostgreSQL, Kingbase, and MySQL use the existing
`(dictionary_id,parent_id)` index. MySQL's configured recursion ceiling may
reject extremely deep corrupt or unsupported trees; this fails closed rather
than returning an incomplete candidate list. The update endpoint's existing
cycle check remains authoritative under concurrent edits.
