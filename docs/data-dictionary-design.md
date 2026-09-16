# Data dictionary module design

## Interface decisions

| Concern | Decision and rationale |
| --- | --- |
| Contract | Dictionaries are `enum` or `tree`, and are backed by `static` items or one registered `provider`. Management uses POST+JSON create/get/page/update/delete operations. Public `POST /api/v1/public/dictionaries/query` returns one normalized result contract for both sources. Enum results are paged; tree results are complete bounded forests because paging would break ancestry. |
| Authentication | Public query allows anonymous and authenticated callers. Management routes use the shared Resource/Action PBAC interceptor. Providers are in-process Go implementations and have no separate transport/authentication surface. |
| Authorization | Public query has the database expression `anonymous || authenticated`. Definition/item management uses `platform.dictionary.manage` with platform scope. Provider implementations must only expose data suitable for this public endpoint. |
| Operation log | Successful definition/item mutations enqueue `dictionary.*` events in the same transaction; failed mutations enqueue a sanitized failure event. Reads are not logged because dictionary values are public presentation data. |
| Security log | No security event is emitted: dictionary configuration is auditable business configuration, not an authentication/session event. Every mutation emits an operation log. |
| Cache | Public results use Redis keys containing dictionary ID/version and a SHA-256 request fingerprint. Definition/item changes increment the definition version; bounded TTL removes unreachable keys. Redis failure degrades to the authoritative database/provider path. |
| Distributed lock | No distributed lock. Definition-row locking, same-dictionary foreign keys, unique constraints and optimistic versions own correctness; provider calls are read-only. |
| Optimistic lock | Definition and item update/delete require a positive expected version. Item mutations also increment the parent definition version for cache invalidation. |
| Audit | Every table has created/updated actor and time, version, and logical-delete fields. PostgreSQL/Kingbase enable the common audit trigger; all writes use the shared transactor and principal context. |
| Presentation | Management responses include persisted audit fields. Public values include ID, code, name, value, disabled state, sort order, parent/tree structure and JSON extension, but omit actor and audit metadata. Timestamps follow the platform Asia/Shanghai presentation convention. |
| Tests | Unit tests cover bounds, sorting, ancestor retention, cycle rejection and in-process provider validation/registration. Testcontainers covers PostgreSQL/MySQL migration, audit fields, optimistic locking, tree search and enum paging without another service. |
| Shared capability | Reuses pagination, cache store, transactions, operation outbox recorder, Resource/Action registry, common envelope and error codes. The Go provider interface and query/result types are the future SDK extraction boundary. |

## Provider protocol

Providers implement `dictionary.Provider` and register by dictionary code in
`dictionary.ProviderRegistry` during Fx construction/startup. There is no
remote provider endpoint or database provider-registration table in this phase.

Requests support:

- dictionary code (the persisted definition supplies `enum` or `tree`);
- case-insensitive search keyword;
- bounded ID/code lists and explicit `include_disabled`;
- enum page/page size, or an unpaged bounded tree;
- allowlisted sorting by `sort_order`, `code`, or `name`;
- a bounded JSON `extension` object passed through without interpretation.

Responses use stable string IDs, code, name, value, optional parent ID,
disabled flag, sort order, nested children for tree dictionaries, and a bounded
JSON extension object. Providers must return no more than 200 enum items per
page or 10,000 total tree nodes, use deterministic ordering, honor context
cancellation, and must not return credentials or authorization-sensitive data.

## Module checklist addition

Every new module and interface must explicitly decide whether it owns data that
should be exposed as a dictionary provider. If yes, it implements the provider
Go interface without exposing its database outside the module; if no, its
design decision records why the data is transactional, sensitive, unbounded,
or otherwise unsuitable.
