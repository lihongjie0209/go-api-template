# Dictionary selectable-value integrity

The selectable value of an item is `value` when non-empty, otherwise its `id`.
Within one dictionary, active items must not share a selectable value. This is
the identity used by public enum/tree queries and by frontend selectors.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Existing POST+JSON item create/update/delete and public query contracts are unchanged. A duplicate active selectable value returns the shared conflict error; a soft-deleted item's value can be reused. |
| Authentication | Item mutations retain JWT and their registered Resource/Action requirements; public query remains anonymous. |
| Authorization | The existing platform dictionary permission is checked before the service call. This invariant does not grant any additional access. |
| Operation log | Existing transactional mutation events and sanitized failure events remain in use; no new event stream is introduced. |
| Security log | None; item configuration is not a credential or session operation. |
| Cache | Successful item mutations still advance the definition version after the write; rejected conflicts do not change it. Query cache remains keyed by that revision. |
| Distributed lock | None. A transaction-local lock on the dictionary definition row serializes item writes at the smallest dictionary scope. Create, update, and delete use the same definition-before-item lock order. |
| Optimistic lock | Update/delete retain their expected item version predicates; value conflict checks do not replace them. |
| Audit | Existing transactor actor context and audit triggers continue to maintain mandatory fields. |
| Presentation | Existing item labels and ID fallback remain unchanged. The frontend shared query API rejects ambiguous responses, including dynamic-provider results. |
| Tests | Local unit tests cover create/update conflicts, ID fallback, lock order, and provider duplicates. PostgreSQL/MySQL Testcontainers lifecycle tests cover conflicts, soft-delete reuse, and concurrent creates in CI; local development does not run containers. |
| Shared capability | Reuses the dictionary service, shared transaction and conflict error; no parallel cache or locking implementation. |
| Dictionary provider | In-process provider responses are rejected when one page/tree contains duplicate effective values. Providers must keep values unique across pages; the public UI validates complete multi-page loads as another guard. |

The service-level lock protects writes through this service, but it does not
prevent a direct SQL writer from introducing duplicates. A database constraint
requires a separate migration review: values may be up to 1 MiB, exceeding a
simple full-text unique B-tree key on PostgreSQL/Kingbase, and MySQL's `TEXT`
cannot be fully indexed as a straightforward unique key. Existing duplicates
must be audited before such a migration. Until then, direct writes to
`dictionary_items` remain prohibited and the frontend fails closed on
ambiguous results.
