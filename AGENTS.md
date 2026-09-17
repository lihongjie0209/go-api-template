# Go Microservice Engineering SOP

This file is mandatory project-level guidance for every human and coding agent
working in this repository. It applies to the template and to every service
generated from it.

## Service boundaries

- A service is the unit of ownership, deployment, testing, migration, and
  maintenance.
- A service must not query another service's transactional tables. Use its
  HTTP/gRPC API. Direct cross-service database reads are allowed only for an
  explicitly reviewed OLAP/reporting pipeline.
- Unit and integration tests must be self-contained within one service. Tests
  must not require another business service to be running.
- Shared infrastructure contracts and reusable implementations belong in the
  platform SDK. Before adding local infrastructure code, check whether the SDK
  already provides it. Improvements that are useful to multiple services must
  be proposed for extraction instead of copied.

## User and tenant domain boundaries

The next domain work must keep identity and tenancy separate:

- The user/identity module owns users, credentials, login, token issue and
  refresh, logout, session revocation, JWKS, service accounts, account status,
  MFA/recovery hooks, and security events.
- The tenant module owns tenants, organizations, departments, memberships,
  invitations, tenant status, and the user's active tenant context.
- Identity must not write tenant membership tables. Tenant membership must
  reference a user ID and obtain display data through the identity API or a
  locally maintained projection.
- Authorization consumes normalized principal, tenant, membership, resource,
  and action information. Identity and tenant modules do not embed independent
  RBAC implementations.
- Login can occur without an authenticated principal. Security logging for
  login must accept an explicit target/identifier digest and must never require
  user context before authentication succeeds.
- Tenant isolation is a mandatory SQL/repository boundary, not only a service
  authorization check. Every query, update, delete, count, join, cache key, and
  uniqueness rule for a tenant-owned table must include the authenticated
  `tenant_id`; association tables are not exempt. The tenant root table is
  scoped with `tenants.id = current tenant_id`. Repository methods for normal
  traffic must require a non-empty tenant ID and tests must prove another
  tenant's rows are invisible. Cross-tenant platform administration must use
  separately named admin methods/endpoints with explicit platform permission
  and operation logging; an empty tenant context must never silently mean
  unrestricted access.

## API contract SOP

Every resource must use consistent POST + JSON business APIs. Operational
health and metrics endpoints are the only standard exceptions. File upload may
use multipart on its exact documented route.

Minimum resource operations, when applicable:

- `POST /api/v1/<resources>/create`
- `POST /api/v1/<resources>/get` with an ID request
- `POST /api/v1/<resources>/page`
- `POST /api/v1/<resources>/update`
- `POST /api/v1/<resources>/delete`
- Explicit domain commands such as enable, disable, invite, revoke, or switch
  must be separate operations rather than hidden update side effects.

Get-by-ID requests use `{"id":"..."}`. Update and delete requests must include
`id` and `version`. Page requests use:

```json
{
  "page": 1,
  "page_size": 20,
  "keyword": "",
  "filters": {},
  "sort": [{"field": "created_at", "direction": "desc"}]
}
```

Page numbers start at 1. The default page size is 20 and the configured maximum
must be enforced. Sort and filter fields must use server-side allowlists; never
interpolate client-provided SQL identifiers. Page responses use:

```json
{
  "items": [],
  "page": 1,
  "page_size": 20,
  "total": 0
}
```

Before finalizing a list/page contract, review every meaningful resource field
against frontend query needs. Explicitly decide whether it supports exact
matching, case-insensitive fuzzy matching, multi-value/`IN` matching (especially
IDs, statuses, types, and related IDs), and range matching (especially time and
numeric ranges). Keep `keyword` for documented cross-field search and expose
typed filters for precise queries. Define empty-list semantics, maximum `IN`
size, range boundaries, timezone interpretation, index support, filter
combinations, stable sorting, and tenant/data-permission interaction. Record a
reason for fields intentionally not queryable, and test representative filter
combinations at unit and integration levels.

All responses use the shared `{code,message,body,request_id}` envelope and
global error codes. Do not create service-local equivalents for common errors.
OpenAPI annotations, request validation, authorization requirements, and error
responses are part of the interface implementation.

## Mandatory per-interface design review

Before implementing an interface, add a short design decision table to the
task, PR, or design document. Every row requires a concrete decision and
rationale; “considered” is not a decision.

| Concern | Required decision |
| --- | --- |
| Contract | Operation name, request/response model, validation, error codes, pagination/sort/filter rules |
| Authentication | Public, JWT, PSK, or internal-only; wildcard policy where applicable |
| Authorization | Resource, action, scope, and object/tenant ownership checks |
| Operation log | None or recorded; event name, resource type/ID, safe request summary, success/failure |
| Security log | None or recorded; mandatory for authentication, token, session, credential, privilege, membership security, force/revoke, and suspicious actions |
| Cache | None or cached; key namespace, TTL, invalidation owner, miss/penetration strategy, consistency expectation |
| Distributed lock | None or locked; exact contention invariant, smallest possible key, TTL, retry policy, lock-loss behavior |
| Optimistic lock | Required for normal update/delete/state transition using `WHERE id=? AND version=?`; document any exception |
| Audit | Actor source and maintenance of all mandatory audit fields |
| Presentation | Time format/timezone, enum labels, related display names, localization, and masking |
| Tests | Unit cases, integration cases, concurrency/failure cases, and regression mapping |
| Shared capability | Existing component reused; candidate extraction or improvement identified |

Every module and interface must also decide whether its owned data should be
exposed through the shared data-dictionary provider contract. A provider must
implement the bounded search/filter/sort/tree/extension contract as an
in-process Go implementation registered by code. Do not add a remote provider
transport until cross-service dictionaries are explicitly required. Record
an explicit reason when the data is transactional, sensitive, unbounded, or
otherwise unsuitable for dictionary use.

### Permission design is mandatory

- Every module must maintain a permission matrix covering every HTTP operation,
  gRPC method, scheduled/internal command, and sensitive object action. Each row
  declares public/internal/JWT/PSK authentication, canonical permission key,
  resource, action, scope (platform/tenant/principal), object ownership, and
  data filtering. “Authenticated only” is not a substitute for authorization.
- Permission identifiers use stable lowercase semantic names such as
  `platform.tenant.update`; UI route names and HTTP paths must not become the
  authorization contract. Renaming or removing a permission requires a
  migration and compatibility review because roles may reference it.
- Services use the shared SDK policy registry and authorization interceptors.
  Business routes and RPCs not explicitly declared public or assigned a
  permission must fail closed with the global policy-missing error. Never add a
  local allowlist or duplicate authorization middleware.
- Permission checks occur before handlers/services mutate state. Object-level
  and tenant-level SQL filtering remains mandatory after the permission check;
  a permission grant does not bypass data isolation.
- Unit tests must cover allow, deny, missing principal, decision-service
  unavailable, and undeclared-route fail-closed behavior. CI must compare the
  registered permission matrix with HTTP/gRPC routes and OpenAPI/protobuf
  operations to catch omissions.
- Operation authorization and data permission are separate modules and policy
  models. Operation policies answer whether a subject may invoke a resource
  action and use boolean deny-overrides. An operation policy may use `when`
  only for bounded server-derived subject, tenant, endpoint, authentication,
  and environment attributes such as trusted local time; it must never inspect
  request bodies, current resource fields, or proposed mutations. Data-permission
  policies answer which rows that already-authorized action may access; matching
  Allow predicates are unioned and the union of Deny predicates is subtracted.
  Never put a data condition in an operation policy or treat an operation Allow
  as unrestricted row access.
- Data-permission `condition` applies to the current persisted row and must be
  compilable into the shared parameterized SQL Predicate. Optional
  `proposed_condition` applies only to a server-validated, server-constructed
  create/update target object and is evaluated in memory after the current row
  was selected under tenant, logical-delete, version, and SQL data scope. Raw
  request JSON is never a trusted proposed object. Create operations use the
  constructed new object as both current and proposed input. Read/list/delete
  actions must reject `proposed_condition` because no target state is evaluated.
- Page/list/count/get/update/delete repositories must apply the compiled data
  scope together with tenant isolation and logical-delete predicates. Items
  and counts use the same scope; filtering after pagination is forbidden.
  Policy text and client input never become SQL identifiers or SQL fragments.
- Every HTTP route and gRPC method must bind an explicit authorization
  descriptor in the same code registration call: public/internal/authenticated
  mode, canonical resource, action, and whether data permission is required.
  Missing metadata fails startup and CI. Paths and handler names are not policy
  identities; after complete descriptor coverage and decision migration, remove
  the legacy database route-policy module rather than maintaining two sources
  of authorization truth.
- Frontend authorization uses server-side capability evaluation only. Page
  capabilities evaluate canonical resource/action pairs against registered
  target operations. Row capabilities accept only bounded resource IDs and
  actions; the owning module registers a shared `RowCapabilityProvider`, loads
  trusted rows in one tenant-scoped batch, and evaluates current-row data
  permission. Provider results must be a subset of requested IDs and each
  trusted `id` attribute must equal its map key. Never send policy expressions or
  accept client-supplied resource/subject attributes. Capability results are UI
  hints with a policy revision and short expiry; every business endpoint must
  repeat authoritative authorization.
- Row capability registration fails startup and CI unless the provider's
  resource exists in the canonical Resource/Action registry, has tenant scope,
  and owns a registered data-permission Schema. A provider must not silently
  introduce an untyped or platform-scoped row-evaluation path.

## Logging decisions

- Query-only operations normally do not emit operation logs unless they expose
  sensitive data, export large datasets, or are explicitly auditable.
- Create, update, delete, state transitions, import/export, and administrative
  commands normally emit operation logs through `operationlog.Recorder`.
- Login, token refresh, logout, forced logout, session or credential changes,
  MFA, service-account key changes, privilege changes, membership revocation,
  and suspicious authentication activity must emit security logs through the
  independent `securitylog.Recorder`.
- Log persistence is asynchronous through the configured queue. Business code
  must not write operation/security log tables directly.
- Never record passwords, secrets, bearer tokens, refresh tokens, cookies, or
  raw credentials. Use the shared sanitization and keyed digest facilities.
- Security-critical successful actions default to fail-closed when their
  security event cannot be enqueued. Any fail-open exception requires an
  explicit product/security decision.

## Cache SOP

- Cache read-heavy, write-light, slowly changing data only when it materially
  improves latency or dependency load.
- Use the platform SDK's shared `cache.Store`; service-local cache packages may
  add configuration and observability decorators but must not copy the Redis
  implementation. Business code must not depend directly on Redis.
- Cache keys must include service namespace, tenant where applicable, resource
  type, ID, and a schema/version segment.
- The service that owns the write owns invalidation. Successful commits happen
  before cache invalidation or refresh.
- Define TTL, negative-cache behavior, stampede protection, stale tolerance,
  and failure policy. Do not cache authorization-sensitive responses without
  including all relevant principal/tenant dimensions.
- Never put IDs, cache keys, or unbounded values into metric labels.

## Redis and message queue decision SOP

Choose by delivery semantics, not by convenience or which client is already
available. A feature must not introduce a second transport for the same event.

- Use Redis for derived, disposable, and reconstructable state: cache values,
  cache invalidation hints, distributed locks, rate-limit counters, idempotency
  state, short-lived sessions, and ephemeral coordination. Correctness must not
  depend on Redis Pub/Sub delivery because subscribers can disconnect and
  messages are not replayed.
- Use the message queue for durable business facts and work that must eventually
  run: domain integration events, operation/security/audit log ingestion,
  notifications, exports, billing, workflows, and cross-service state changes.
  Consumers must acknowledge only after successful handling and must be
  idempotent because at-least-once delivery can produce duplicates.
- Cache invalidation within instances of one service uses Redis Pub/Sub for
  low-latency refresh plus a database revision/version poll for lost-message
  recovery. The database remains authoritative; refresh builds a complete new
  snapshot and swaps it atomically only after validation succeeds.
- A durable cross-service notification must use a transactional outbox written
  in the same database transaction as the business change and then be published
  to the message queue. Do not dual-write the database and queue directly.
- Do not use Redis lists/streams as an ad-hoc business queue unless an explicit
  architecture decision accepts its operational and delivery tradeoffs. Do not
  use the durable message queue for request-path cache reads, locks, rate limits,
  or other synchronous coordination.
- A Redis outage may reduce performance or temporarily reject coordination-heavy
  operations, but must not silently corrupt authoritative business data. A queue
  outage must retain pending outbox records and recover delivery later.
- Every asynchronous flow documents ordering scope, retry/backoff, dead-letter
  handling, retention, payload version, idempotency key, observability, and PII
  policy. Every Redis use documents key namespace, TTL, memory bound, failure
  policy, and authoritative recovery source.

Decision shortcut: if losing the signal is safe because state can be reloaded,
use Redis; if losing it would lose a business action or audit trail, use the
message queue with an outbox.

## Idempotency SOP

- Mutation endpoints explicitly opt in; query/list endpoints must not use an
  idempotency key as a cache substitute. HTTP and unary gRPC use the shared SDK
  state machine and the same semantic rules.
- Fingerprints include service, transport operation, canonical request data,
  principal type/ID, tenant, membership, and session dimensions. Reusing a key
  across tenant or principal contexts must conflict rather than replay data.
- Processing ownership uses an unguessable owner token and a renewable TTL.
  Business work must propagate the lease Context and stop when Redis is
  unavailable or ownership is lost. Completion/failure transitions verify the
  owner atomically.
- Define processing, completed-result, and failure TTLs, maximum stored response
  size, retryable-failure behavior, and Redis-outage policy. Never persist
  credentials, tokens, cookies, signed URLs, or unbounded response bodies.
- The authoritative write commits before a result is published. Database unique
  constraints, transactions and optimistic versions must make a retry safe;
  Redis idempotency cannot create atomicity with the business database.
- Tests cover equivalent-payload canonicalization, changed-payload conflicts,
  tenant/principal isolation, concurrent processing, lease renewal/loss,
  stale-owner completion, replay, expiry, and both HTTP/gRPC contracts.

## Distributed lock SOP

- A distributed lock is for a concrete cross-instance invariant, not a default
  wrapper around every write.
- Use the platform SDK's shared `cache.Locker` backed by the approved mature
  component; do not copy lock algorithms into a service.
- Lock keys must use the smallest conflicting resource, for example
  `tenant:<tenant-id>:membership:<user-id>`, never one global service lock.
- Define acquisition timeout, lease TTL, extension point, failure behavior, and
  what happens when ownership is lost.
- Protected callbacks use the shared `cache.WithLock` renewal lifecycle and
  must propagate its callback context to database, Redis, and upstream calls.
  Lease loss cancels that context. Direct `Lock`/`Extend` use requires an
  explicit, tested lifecycle owner.
- The protected operation must remain idempotent and must still rely on
  database constraints/transactions. A distributed lock is not a correctness
  substitute for those controls.

## Outbound service call SOP

- Services use the Fx-managed named HTTP/gRPC registry. Do not construct an
  ad-hoc client per request or bypass the shared timeout, authentication,
  telemetry, retry, and circuit-breaker policies.
- Every unary call has one bounded end-to-end budget covering connection,
  retries, backoff, headers, and response processing. A shorter caller
  deadline always wins. HTTP callers must close response bodies; streaming
  gRPC callers must set an explicit operation deadline and own cancellation.
- Automatic retries are bounded and jittered. HTTP retries only semantic
  idempotent verbs or a call carrying `Idempotency-Key`; gRPC retries only a
  reviewed method pattern or a call carrying the shared idempotency key.
  Streaming RPCs are never transparently retried or circuit-broken.
- Bearer/PSK credentials require TLS except for an explicit non-production
  development opt-in. Base URLs must not contain credentials, query secrets,
  or fragments. Shared authentication replaces caller-provided authorization
  values rather than appending a second value, and HTTP redirects are never
  followed automatically with credentials.
- TLS uses TLS 1.2 or newer, retains system trust when adding a private CA, and
  requires client certificate/key pairs together. Production credentials
  cannot use the plaintext opt-in.
- gRPC resolver targets use round-robin balancing; HTTP uses the standard DNS
  and connection pool. More advanced discovery must be added behind the shared
  registry, not embedded in business services.
- Metrics use only bounded client names, protocol, result, and duration. Trace,
  Request ID, and idempotency metadata propagate from the caller Context;
  credentials, targets, request bodies, and tenant/user IDs are not labels.
- Tests cover deadline precedence, cancellation, response-body lifecycle,
  retry eligibility/exhaustion, jitter-compatible bounds, breaker opening,
  authentication replacement, correlation metadata, unsafe TLS/URL rejection,
  and direct-constructor validation.

## Database, audit, and optimistic locking

Every table, including association, outbox, inbox, operation-log, security-log,
and job tables, must contain:

```sql
created_at timestamptz NOT NULL,
created_by text NOT NULL,
updated_at timestamptz NOT NULL,
updated_by text NOT NULL,
version bigint NOT NULL,
deleted_at timestamptz,
deleted_by text
```

- Prefer PostgreSQL `text` unless a length constraint is a real domain rule.
- PostgreSQL/Kingbase tables must call `app_enable_audit('<table>')`.
- Writes must use `database.Transactor.Within`, which injects the actor ID
  from context into transaction-local `app.actor_id`.
- Background jobs must create an explicit system principal context.
- Triggers maintain timestamps, actor IDs, version increments, and soft-delete
  metadata. Physical deletes from audited tables are forbidden.
- Update, delete, and state-transition SQL must include the expected version in
  its predicate. Zero affected rows map to the shared version-conflict error.
- Normal reads must filter `deleted_at IS NULL`.
- Database constraints remain the final integrity boundary.
- Every service owns a unique migration history table. PostgreSQL/Kingbase
  services may share a database only through separate schemas.

### Stable IDs for initialized business data

- Initialized records with a durable business identity must use a deterministic
  hash-based UUID (UUID v5), never a random UUID, database sequence, row order,
  environment name, deployment time, or migration version. This applies to
  applications, menus, routes, permission definitions, dictionary types and
  entries, built-in roles, platform configuration definitions, scheduled-job
  definitions, and other seed/reference data that is addressed across services,
  environments, migrations, tests, frontend code, or external integrations.
- Use the shared stable-ID helper from the platform SDK. Do not implement UUID
  hashing independently in each service. The helper contract is conceptually
  `UUIDv5(namespaceUUID, canonicalBusinessKey)`; UUID v5/SHA-1 is used only as a
  deterministic identifier construction mechanism, never for passwords,
  signatures, secrets, integrity checks, or other security decisions.
- Every platform and service owns an immutable, explicitly documented namespace
  UUID. Namespace UUIDs are constants shared by all environments. Do not derive
  the namespace from a mutable service display name, Kubernetes namespace,
  domain, database/schema name, active profile, tenant ID, Git repository path,
  or deployment environment.
- Canonical business keys use lowercase ASCII semantic segments separated by
  `:` and must include the resource kind, for example
  `application:identity`, `permission:platform.user.update`,
  `menu:system:user-management`, or `dictionary:user-status:active`. Trim
  whitespace before validation; do not silently transliterate, collapse, or
  otherwise normalize invalid input. The canonical key is an immutable machine
  identity, not a display name, URL, translated label, sort order, or database
  primary key.
- Tenant-owned seed data must include an immutable tenant business identifier in
  the canonical name only when the record is intentionally distinct per tenant.
  Runtime tenant records and ordinary transactional data continue to use random
  UUIDs. Never include environment/profile in either form, so promotion from
  development to test and production preserves IDs.
- Seed migrations are idempotent and upsert by the deterministic ID while still
  validating the canonical business key for uniqueness. Re-running migrations,
  resetting a database, generating a new service from the template, or loading
  fixtures must reproduce the same IDs. Foreign-key seed data derives referenced
  IDs through the same helper instead of copying unexplained UUID literals.
- Changing a canonical key or namespace is an ID migration, not a rename. It
  requires an explicit compatibility plan that updates all foreign keys,
  authorization grants, caches, events, frontend references, and external
  consumers. Display names, menu URLs, icons, localized labels, and sort order
  may change without changing the canonical key or deterministic UUID.
- Unit tests must pin representative canonical-key-to-UUID mappings as golden
  contract values. Integration tests must prove seed loading is repeatable and
  does not create duplicates. CI must reject duplicate namespace UUIDs,
  duplicate canonical keys, invalid key formats, random UUID generation in seed
  definitions, and drift in pinned stable IDs.

## Frontend presentation contract

- Persist timestamps as timezone-aware values. The platform default timezone
  for display and configured database sessions is `Asia/Shanghai` (UTC+8).
- API timestamp fields use RFC 3339 with an explicit offset. Do not return
  locale-formatted date strings from repositories.
- Do not make the frontend display opaque related IDs. A response containing a
  related ID must also contain a stable display field such as
  `tenant_name`, `user_name`, `department_name`, or a structured
  `{id,name}` reference.
- Snapshot display names into logs/history when historical readability matters;
  otherwise resolve through an owned projection or service API. Do not join
  another service's transactional database.
- Enums returned to clients must have documented codes and display labels or a
  dictionary contract.
- Sensitive identifiers and personal data must use the shared masking rules.

## Tree resource contract

- Tree APIs return ordered nodes using one shape: the resource fields plus a
  non-null `children: []` array. Flat/page APIs remain available separately;
  never paginate a nested tree implicitly.
- Flat records use nullable `parent_id`; roots have `parent_id = null`. IDs,
  parent IDs, sibling `sort_order`, and a deterministic ID tie-breaker define
  stable output. The server builds trees with the public SDK `tree` package.
- Create/update must reject a missing parent, self-parenting, cross-scope or
  cross-tenant parents, and moving a node under any descendant. Delete must
  explicitly choose leaf-only, subtree soft-delete, or reparent behavior; the
  default is leaf-only.
- Tree reads must have a configured maximum node count and must apply tenant,
  application, status, authorization, and logical-delete filters before tree
  construction. Never fetch a global tree and filter it afterward.
- Search results are flat by default. If an endpoint promises tree search, it
  must document whether ancestors are included for context and whether
  unmatched descendants are omitted.
- Do not implement local recursion helpers. Reusable construction, validation,
  traversal, flattening, ancestor, descendant, and path functions belong in
  the shared SDK with cycle/orphan/duplicate tests.

## Testing SOP

- Every feature and interface requires unit tests. Cover validation, success,
  not-found, authorization/tenant isolation, version conflict, dependency
  failure, cancellation, cache behavior, lock contention/loss, logging
  decisions, and sensitive-data redaction as applicable.
- Repository and adapter behavior requires Testcontainers integration tests
  using the `integration` build tag. Cover PostgreSQL and MySQL compatibility
  where the feature supports both; cover Kingbase-specific SQL through the
  closest available compatibility test plus target-environment CI.
- Integration tests must not call another business service. Replace external
  service dependencies with an in-process fake/server or test only the local
  adapter contract.
- Local development runs ordinary unit tests. GitHub CI runs Testcontainers
  integration tests. Do not block unrelated development waiting for CI; check
  results later and fix failures promptly.
- Every defect first found by integration or manual testing must receive the
  smallest practical unit regression test before or together with the fix.
- Generated Swagger/protobuf drift, migration audit contracts, race tests,
  vet, lint, and Kubernetes validation remain required CI gates.

## Shared-component review

For every implementation:

1. Search the platform SDK and existing infrastructure packages first.
2. Reuse the shared response, error, principal, authorization, audit,
   transaction, cache, lock, object-storage, event-bus, operation-log,
   security-log, observability, pagination, and client abstractions where
   applicable.
3. Do not fork or copy a common component into a business package.
4. If local work reveals a reusable capability, add tests and propose it for
   the public SDK with a stable interface.
5. If an existing shared component is insufficient, strengthen it centrally
   and migrate callers rather than creating a parallel implementation.

## Definition of Done

An interface is not complete until:

- its design-review table has explicit answers;
- database migration and audit contract pass;
- authentication, authorization, tenant isolation, log, cache, lock,
  optimistic-lock, and presentation decisions are implemented;
- unit tests and applicable integration tests exist;
- OpenAPI/protobuf documentation is regenerated and consistent;
- metrics/traces use bounded labels and exclude sensitive data;
- shared components were reused or an extraction decision was recorded;
- `go test ./...`, `go vet ./...`, formatting, and CI policy checks pass.
