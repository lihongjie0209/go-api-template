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
- Identity lookups that decide whether a user may become a tenant owner or
  member must read current database status and logical-delete state. A cached
  user profile is only for presentation; failed cache invalidation must not
  make a disabled or deleted identity eligible. The tenant client must also
  reject any non-active identity response and malformed/empty user ID before
  constructing an owner or membership write.
- Authorization consumes normalized principal, tenant, membership, resource,
  and action information. Identity and tenant modules do not embed independent
  RBAC implementations.
- Login can occur without an authenticated principal. Security logging for
  login must accept an explicit target/identifier digest and must never require
  user context before authentication succeeds.
- Both locally signed and remotely verified bearer principals must have a
  supported type and non-empty subject. User principals require a session ID;
  tenant and membership IDs must be present together. Service accounts must
  not carry user session, tenant, or membership context. Validate this before
  attaching any principal to an HTTP or gRPC request.
- Tokens issued by the template must verify with the versioned platform SDK
  JWKS verifier for both RS256 and ES256. Remote JWKS initialization must fail
  startup when its first fetch fails or exceeds the configured bounded timeout;
  the verifier's long-lived refresh context must remain active until shutdown.
- During key rotation, an unknown `kid` with a healthy JWKS endpoint is an
  invalid token. A failed remote refresh is an authentication-dependency
  outage (HTTP 503 / gRPC Unavailable), not an invalid token (401); preserve
  this distinction without exposing token or endpoint details to clients.
- Login implementations must distinguish invalid credentials or concurrent
  credential revocation from database, signer, or security-audit failures.
  Infrastructure failures fail closed with dependency-unavailable, never an
  invalid-credentials response; no issued token is returned before the
  successful-use write and security event commit together.
- Refresh-token rotation must retain server-side digest lineage for the full
  live session, not only the immediately previous token. Any recorded older
  token is a replay that revokes its session and records one transactional
  security event; an unknown token must not revoke an unrelated session.
  Rotation, lineage insert, versioned session update and success audit commit
  atomically, and expired lineage is cleaned in bounded audited batches.
  Configuration must cap the refresh-token lifetime; the template maximum is
  90 days for both file and environment overrides.
  Access-token lifetime must be between 1 minute and 24 hours, must not exceed
  the refresh-session lifetime, and must be checked again at the signer boundary
  so a direct constructor cannot issue an unsafe token.
- Failed-login counters are an explicit high-concurrency exception to an
  expected-version predicate: use one atomic SQL increment guarded by the
  observed credential hash and current active/unlocked state. This lets
  simultaneous wrong attempts count while preventing an attempt against an
  old password/secret from locking a newly rotated credential. A skipped
  increment still records the failed authentication security event.
- Both service-account and user-login rate-limit dimensions fail closed when
  Redis is unavailable or the client key cannot be established, even if the
  general API limiter is configured to fail open. Authentication routes must
  register the dedicated `LoginRateLimit` middleware; the fail-closed choice
  must not depend on the dimension's spelling.
  Production configuration must require the limiter to remain enabled.
- Signed-token state checks must fail closed while distinguishing an inactive
  session/account from an unavailable authoritative store. An infrastructure
  outage returns dependency-unavailable (HTTP 503 / gRPC Unavailable), not an
  invalid-token response that could make clients discard valid credentials.
- Remote JWKS verification must have an explicit bounded per-request deadline;
  timeout/cancellation must not be classified as an invalid signature. The
  caller's shorter deadline always wins.
- Service-account bearer tokens must not carry user session, tenant-membership,
  or password-change claims. Enforce this both when signing local tokens and
  when accepting local or remotely verified tokens; do not rely on downstream
  authorization to ignore unexpected context fields. The shared remote JWKS
  verifier returns only a normalized principal; after successful signature
  verification, inspect the authenticated token for service-account-specific
  claims that are absent from that principal.
- Service-account bearer tokens must carry a positive credential generation.
  Secret rotation increments the authoritative account generation in the same
  transaction as the secret hash update. Status or expiry changes also increment
  it so re-enabling an account cannot revive an old bearer token; presentation
  edits do not. Local and remote signature paths must
  compare the signed generation with the active account row on every request;
  a stale or missing generation is invalid, while a database outage remains an
  authentication-dependency failure. The general audit/optimistic version is
  unsuitable for this check because ordinary login also increments it.
- Bearer JWTs must be bounded before local parsing, remote JWKS verification,
  or database state checks. Oversized or empty tokens are invalid credentials,
  not an authentication-dependency outage. The signer must not return a token
  that the verifier's bound would reject.
- HTTP business handlers must use the shared response helpers. They must not
  write a success or dependency error after their request Context expires;
  an elapsed deadline becomes the global request-timeout envelope, and a
  disconnected client receives no attempted late response. Middleware that
  writes a saved or captured response, especially idempotency replay, must
  enforce the same deadline before writing it.
- gRPC ingress must keep a bounded receive-message size and metadata/header
  list. Configuration overrides cannot raise the receive size above 64 MiB;
  the server accepts at most 64 KiB of uncompressed header metadata.
- A signed access-token claim is not authoritative for mutable credential
  state. Both local and remotely verified user tokens must recheck the active
  session, user, credential and any tenant membership before attaching the
  principal; service-account tokens must recheck the active account. A missing
  authoritative state source fails closed as dependency-unavailable. Every
  password reset path must revoke existing sessions in the same transaction,
  including the path that creates a missing credential.
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
Every business POST must declare `Content-Type: application/json`, including
requests with an empty body. Only the exact file-upload route may declare
multipart. Liveness and readiness probes do not require a JSON content type.

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

Keyword/fuzzy searches treat client text literally: `%`, `_`, and the chosen
SQL `LIKE` escape character must not act as caller-controlled wildcards. Use
the shared `database.LikeContainsPattern` helper with `ESCAPE '!'` on every
`LIKE` branch, including count and page queries; preserve bound parameters.
Static SQL searches and in-process dictionary providers must agree on this
matching behavior. Tests must cover `%`, `_`, and `!` against both PostgreSQL
and MySQL where the query supports both.

Batch lookup operations with an empty ID list must return an empty collection
without issuing an unfiltered page/list query. They must still enforce the
normal authentication boundary. An omitted optional page filter is not the
same contract as an explicitly empty batch lookup.
An internal adapter that converts optional search filters into candidate IDs
must reject missing or blank filters before calling a general page API; an
empty filter must never silently become an unrestricted candidate scan.

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

Health probes must fail closed for configured mandatory dependencies: a missing
client handle is `down`, not `disabled`. Report `disabled` only when the
dependency is explicitly disabled by configuration. Never infer queue consumer
readiness merely from a retry loop; it requires an authoritative subscription
ready signal from the transport. After a readiness check, honor the caller's
ended Context before writing a dependency response: cancellation writes no
body, and an expired deadline uses the shared timeout envelope rather than a
misleading dependency-down result.

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
Provider results must honor every requested keyword, ID, code, value, disabled,
and sort filter. A tree may additionally include only ancestors of matching
nodes; its total is the number of returned nodes, including those ancestors.
Dictionary result limits must bound aggregate serialized item data as well as
row count and tree depth. Static queries must stop materializing rows when the
aggregate budget is exceeded; in-process providers and cache hits must be
validated against the same budget before returning a public response.
Validate these invariants at the public dictionary boundary, even for an
in-process provider. Bound the number of sort fields and reject duplicate
fields before building SQL or calling the provider. Provider item codes are
unique within the dictionary, just like static item codes. Enum page metadata
must use the platform pagination limits; reject an invalid page before
invoking a provider. Do not require `total` to cover the page offset unless
count and page are read from the same snapshot, because concurrent writes may
change the rows between those queries.
The dictionary module may cache static entries by definition revision. It must
not place dynamic-provider results in that shared cache: the provider owns its
data freshness and may depend on request context that is absent from the
definition-only cache key. A provider that needs caching must own scoped keys,
TTL, and invalidation for its own data.

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
- Services use the shared SDK resource/action registry and authorization interceptors.
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
- Credential-state restrictions such as mandatory password change, disabled
  accounts, and revoked sessions must have equivalent enforcement for HTTP and
  gRPC unary/stream business operations. Test each transport explicitly;
  infrastructure health probes remain independently available.
- Operation authorization and data permission are separate modules. Operation
  authorization maps a registered resource/action to one or more permissions,
  then checks platform grants, tenant ceilings and role grants; it fails closed
  when no grant applies. A permission may cover multiple resource/actions.
  Data-permission policies answer which rows an already-authorized action may
  access; matching Allow predicates are unioned and the union of Deny predicates
  is subtracted. A permission grant never implies unrestricted row access.
- Data-permission `condition` applies to the current persisted row and must be
  compilable into the shared parameterized SQL Predicate. Optional
  `proposed_condition` applies only to a server-validated, server-constructed
  create/update target object and is evaluated in memory after the current row
  was selected under tenant, logical-delete, version, and SQL data scope. Raw
  request JSON is never a trusted proposed object. Create operations use the
  constructed new object as both current and proposed input. Read/list/delete
  actions must reject `proposed_condition` because no target state is evaluated.
- Simulating an immutable saved policy version must load its document through
  the scope-limited repository, verify its code and scope against the parent
  policy, and bind tenant-owned subject/resource inputs to the authenticated
  tenant. The request must not carry a replacement policy document.
- Page/list/count/get/update/delete repositories must apply the compiled data
  scope together with tenant isolation and logical-delete predicates. Items
  and counts use the same scope; filtering after pagination is forbidden.
  Policy text and client input never become SQL identifiers or SQL fragments.
- Every HTTP route and gRPC method must bind an explicit authorization
  descriptor in the same code registration call: public/internal/authenticated
  mode, canonical resource, action, and whether data permission is required.
  Missing metadata fails startup and CI. Paths and handler names are not policy
  identities. Resource/action definitions owned by code are reconciled at
  startup; only code-owned definitions absent from code are disabled. Manual
  definitions remain independently manageable.
- Frontend authorization uses server-side capability evaluation only. Page
  capabilities evaluate canonical resource/action pairs against registered
  target operations. Row capabilities accept only bounded resource IDs and
  actions; the owning module registers a shared `RowCapabilityProvider`, loads
  trusted rows in one tenant-scoped batch, and evaluates current-row data
  permission. Provider results must be a subset of requested IDs and each
  trusted `id` attribute must equal its map key. Never send policy expressions or
  accept client-supplied resource/subject attributes. Capability results are UI
  hints with a principal-, session-, and tenant-bound revision covering
  effective operation grants and the published data-permission snapshot, plus
  a short expiry. The operation decision and revision must use the same grant
  snapshot. The data-policy revision and every row-action decision in a batch
  must likewise use one pinned immutable policy snapshot, even during a
  concurrent refresh; a failed authoritative grant read fails closed. A missing
  grant-snapshot provider must fail startup,
  including in generated services and test compositions; do not pass a typed
  nil revision provider through an interface. Every business endpoint must
  repeat authorization.
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
- Rejected authentication, token and session operations must emit a bounded,
  stable failure `reason` and `error_code` plus request/client metadata where
  available; never copy raw dependency errors or tokens into those fields.
  If the service has already committed a transactional security event (for
  example refresh-token reuse), the transport must not enqueue a duplicate.
- Log persistence is asynchronous through the configured queue. Business code
  must not write operation/security log tables directly.
- Operation/security log tables retain the mandatory audit and logical-delete
  columns. Only the reviewed data-lifecycle maintenance path may physically
  purge expired MySQL log rows in bounded batches or detach/archive/drop
  expired PostgreSQL/Kingbase log partitions after the configured retention
  cutoff. Business APIs and queue consumers must not physically delete logs.
- Audit-log consumers must distinguish duplicate event IDs from all other
  database errors. A duplicate may be acknowledged only when a specific
  database uniqueness constraint proves that the same event was already
  persisted; coercion, truncation, trigger and connectivity failures must
  propagate so the queue can retry or dead-letter. Do not use MySQL
  `INSERT IGNORE` or an untargeted conflict handler on durable log writes.
- An audit consumer subscribed to a dedicated event subject must reject an
  envelope with the wrong event type. Returning success would acknowledge and
  discard a malformed durable event before the queue can retry or dead-letter it.
- Queue consumer error callbacks must use the shared safe-error logger; SDK
  callbacks may carry handler errors, event identifiers, or connection details.
  Never forward their raw error text to application logs or traces.
- Long-running queue consumers must retry unexpected subscription exits with
  bounded jittered backoff and stop promptly when their lifecycle Context is
  canceled. A transient broker or subscription failure must not permanently
  disable operation, security, or scheduled-job event consumption. Count actual
  restart attempts with code-owned, bounded consumer names; do not turn an
  unproven subscription attempt into a healthy readiness signal.
- Never record passwords, secrets, bearer tokens, refresh tokens, cookies, or
  raw credentials. Use the shared sanitization and keyed digest facilities.
- Persisted failure fields such as outbox `last_error`, dead-letter metadata,
  logs, and Trace attributes must use stable, safe reason codes. Never copy an
  upstream `error.Error()` into them; dependency errors can contain credentials,
  connection strings, payload fragments, or personal data. Keep event IDs,
  attempts, and bounded component names for correlation instead.
- Fixed failure reasons passed to `observability.SafeError` must be code-owned
  lowercase ASCII identifiers (`[a-z][a-z0-9_]*`, at most 64 bytes). The helper
  rejects malformed reasons and falls back to `operation_failed`.
- The production structured logger additionally fails closed for raw `error`
  and `panic` attributes, including bound and grouped attributes. Callers must
  still classify errors explicitly with `SafeError` so a useful component
  reason survives this last-resort sanitizer; do not rely on it to sanitize
  arbitrary free-text messages or payload attributes.
- Infrastructure spans use `observability.RecordSafeError` with a fixed
  component reason. Only known cancellation/deadline classes may retain their
  original error text; do not record raw Redis, object-storage, queue, or
  handler errors as OpenTelemetry exception events.
- File upload/delete repair and failed-operation logs must use
  `observability.SafeError` with fixed component reasons; database, object
  storage, and queue errors may contain credentials or sensitive object keys.
- HTTP error boundaries, authentication/security-log failures, idempotency,
  and rate-limit middleware must also classify errors before logging. Preserve
  request/trace IDs and bounded operation names for correlation, never the raw
  dependency error or request payload.
- Policy snapshot refresh, revision polling, and Redis invalidation logs must
  retain a fixed failure reason and bounded component/channel identity, never
  raw database or Redis errors. Return the original error only to a caller that
  can handle it without exporting its text.
- Cache fallback, write, and invalidation warnings must use a fixed
  component-owned failure reason. A failed cache read may fall back to the
  authoritative repository as designed, but neither the warning nor the
  fallback response may expose raw Redis errors.
- Panic recovery must never log the recovered value, request headers, body, or
  framework-generated stack/request dump. Emit a fixed panic reason with
  request/trace or bounded RPC method correlation; keep the public response
  generic.
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
- The service that owns the write owns invalidation or the authoritative
  version change that makes old keys unreachable. Successful commits happen
  before cache invalidation or refresh.
- Define TTL, negative-cache behavior, stampede protection, stale tolerance,
  and failure policy. Do not cache authorization-sensitive responses without
  including all relevant principal/tenant dimensions.
- A cache hit must never bypass an authoritative database check that decides
  whether anonymously accessible data is still public and active. For public
  platform configuration, check visibility and version in SQL first, then use
  a version-scoped cache key; an invalidation failure must not expose a value
  after it becomes private, disabled, or deleted. If the database check fails,
  fail closed instead of serving a cached value.
- Identity profile caches may contain personal data. Before returning a cached
  user by ID or username, verify its current version and non-deleted state in
  the Identity database and ensure the cached identity matches the lookup.
  Failed invalidation or database unavailability must not expose a deleted or
  older profile, including email and phone.
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
  snapshot and swaps it atomically only after validation succeeds. If the
  Pub/Sub channel closes, retry subscription at a bounded interval while the
  revision poll continues; do not silently lose low-latency invalidation for
  the rest of the process lifetime or spin in a reconnect loop. Confirm each
  Redis subscription before treating it as active, then immediately check the
  database revision to cover changes between initial snapshot load and the
  subscription acknowledgement. A subscription failure must still allow that
  authoritative revision check to run. Lifecycle Context cancellation during
  a revision read or background snapshot refresh is normal shutdown and must
  not invalidate the snapshot or log a dependency failure; either operation's
  own timeout while the lifecycle remains active must still fail closed.
  A post-mutation refresh uses a separate failure policy: cancellation may
  leave the local snapshot stale, so it must invalidate that snapshot. A
  directly invoked runtime refresh without the sync wrapper must also
  invalidate its prior snapshot after either a database read or policy
  compilation failure. Concurrent direct refreshes without a sync wrapper
  must serialize the load, installation, and failure invalidation so an older
  failed attempt cannot erase a newer successful snapshot. When a runtime
  sync wrapper is configured, direct refresh calls must delegate to it so the
  installed snapshot and its revision advance together.
- A durable cross-service notification must use a transactional outbox written
  in the same database transaction as the business change and then be published
  to the message queue. Do not dual-write the database and queue directly.
- The outbox row ID is the durable event identity. Before publishing a claimed
  row, verify that the encoded envelope carries the same event ID; malformed
  or mismatched rows must follow the bounded retry/dead-letter path without
  publishing. Consumer idempotency keys must use that same canonical ID.
- A request must not synchronously execute work whose configured maximum can
  exceed the HTTP/gRPC deadline or server write timeout. Persist the job request
  and outbox event atomically, return a durable run ID, and expose queued,
  running, and terminal states. A consumer's acknowledgement window must not
  contain a longer-running handler; use a bounded, recoverable worker with an
  ownership lease and a stable business idempotency key.
- Scheduled and queued handler boundaries must recover panics into a fixed,
  non-sensitive failure reason before releasing their distributed lease. Both
  cron and manual runs must persist a terminal failure and operation log;
  recovery logging must never include panic values or stack contents.
- A handler returning `nil` after its execution Context was canceled or its
  deadline elapsed is not a successful run. Cron and manual paths must check
  the Context after the callback before recording status, metrics, and logs.
- Cron executions may skip on lock contention, but an accepted manual run must
  retry contention within its configured execution deadline. If the deadline
  expires, record an error; do not mark a queued manual action as `skipped`.
- A renewal call returning after its task Context was canceled is not proof of
  lease loss. Distinguish cancellation from an active renewal failure: persist
  the terminal timeout result while the database lease is still owned, but
  leave a worker interrupted by service shutdown for lease-based recovery.
- Workers claiming durable manual runs must give expired leases a bounded
  chance to resume even while new queued runs keep arriving. Alternate claim
  preference and fall back to the other class when the preferred class is
  empty; preserve indexed claim queries and `SKIP LOCKED`.
- Do not use Redis lists/streams as an ad-hoc business queue unless an explicit
  architecture decision accepts its operational and delivery tradeoffs. Do not
  use the durable message queue for request-path cache reads, locks, rate limits,
  or other synchronous coordination.
- A Redis outage may reduce performance or temporarily reject coordination-heavy
  operations, but must not silently corrupt authoritative business data. A queue
  outage must retain pending outbox records and recover delivery later.
- Transient outbox publish failures remain pending with bounded retry backoff even
  beyond the configured attempt limit; only deterministic invalid records may
  enter a terminal dead-letter state.
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
- Configured HTTP idempotency patterns must match registered mutation routes at
  startup. Wildcards must not silently include new query, target-preview, or
  public routes; newly added mutation responses still require a secret-data
  review and code-owned approval before being selected. Configuration can only
  select a subset of code-approved routes.
- gRPC idempotency patterns must likewise match registered unary RPCs with
  code-owned response-safety approval. Streaming, health/reflection, reads,
  token issuance, and other sensitive responses must never be replay-cached.
- Public logout uses the refresh token itself as a request credential. It must
  not enter the idempotency replay set: each attempt must check current token
  state and record a failed security event when the credential is revoked.
- Every replay must pass current authentication and operation authorization
  before the idempotency state is read or returned. HTTP idempotency middleware
  must follow route authorization, not precede it as API-group middleware.
- The SDK idempotency lease Context is canceled by its normal stop function.
  After stopping a lease, restore the original HTTP request Context before
  deciding whether to emit a response; lease cancellation is not evidence of
  client disconnect or request timeout. Unit fakes must reproduce this normal
  stop behavior and a lease-loss error separately. If the request expires
  during lease startup, stop the lease, abort the acquired state with bounded
  detached cleanup, and do not invoke the handler. A failed or already-canceled
  lease startup likewise aborts the unstarted acquisition. Once a handler has
  run, do not abort merely because renewal failed: its business outcome may
  already have committed.
- HTTP/gRPC ingress must reject malformed `Idempotency-Key` values before
  business work. Internal callers that require idempotency must validate the
  key explicitly before placing it in Context; the local Context adapter drops
  malformed keys so they cannot propagate upstream or enable automatic retries.
- Place HTTP key validation inside the global request ID, tracing, access-log,
  metrics, security-header, CORS, and timeout middleware. A rejected key is
  still an observable protected HTTP response, even though no business handler
  or idempotency state executes. Apply this validation to `/api/v1/` business
  routes; operational probes and other non-business paths ignore the header so
  an unrelated client or proxy header cannot make liveness/readiness fail.
- HTTP idempotency fingerprinting must stop reading after at most 16 MiB plus
  one sentinel byte, independent of the global body limit used for file
  upload. Reject an oversized JSON mutation before reading or creating any
  Redis idempotency state or invoking the handler.
- Bodyless JSON POST commands may still opt into idempotency. Their empty or
  whitespace-only body has one canonical fingerprint, distinct from explicit
  JSON `null` and `{}`; Content-Type validation still applies before execution.
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
- An opted-in HTTP mutation stages its bounded unified JSON response until the
  owner-fenced Redis Complete/Fail/Abort transition succeeds. Never send a
  successful body before the completed result is saved. If the transition fails,
  return a generic unavailable response and discard handler-only headers such
  as Set-Cookie. Streaming, flushing, and connection hijacking are not supported
  for idempotent HTTP routes. Validate the unified envelope before saving and
  again before replay: success status requires the global success code, error
  status requires a non-success code, and both require a nonblank message.
  Corrupt cached failure states must return generic unavailable instead of
  passing an invalid status or error code to the HTTP writer. Decode captured
  and replayed JSON with number-preserving semantics; routing through float64
  must not change large integer IDs or amounts on replay, including legacy
  cached envelope formats.
- An opted-in unary gRPC mutation must also withhold its response until the
  owner-fenced Complete/Fail/Abort transition succeeds. If persistence fails,
  return generic Unavailable without the handler response or raw dependency
  error. A lease that fails before handler execution must release its acquired
  processing record with bounded detached cleanup; never release an ambiguous
  record after the handler has run. Recheck caller and lease cancellation after
  acquisition and before invoking the handler; aborted unstarted records use a
  bounded detached Context. If the caller cancels after the handler, persist a
  completed result before suppressing the response so a retry can replay it.
  Reject corrupt cached gRPC failure codes or blank messages instead of
  treating them as a successful RPC. Bound stored failure records by the same
  size limit as completed responses.
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
  caller-supplied HTTP headers or gRPC outgoing metadata must not override these
  validated Context values or introduce them when Context has none. Credentials,
  targets, request bodies, and tenant/user IDs are not labels.
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
- MySQL application connections must retain strict SQL mode for every pooled
  session so invalid audit and business values fail rather than being silently
  coerced. Explicit DSN SQL modes may add modes but must not remove strictness.
- Before raising a field's API size limit, verify every supported database
  column can store that many encoded bytes. MySQL `TEXT` is limited to 65,535
  bytes; use an appropriate wider type when the contract exceeds it. Add an
  upgrade migration for existing databases, and make narrowing down-migrations
  fail instead of silently truncating oversized data.
- PostgreSQL/Kingbase tables must call `app_enable_audit('<table>')`.
- Writes must use `database.Transactor.Within`, which injects the actor ID
  from context into transaction-local `app.actor_id`.
- Background jobs must create an explicit system principal context.
- Triggers maintain timestamps, actor IDs, version increments, and soft-delete
  metadata. Physical deletes from audited tables are forbidden.
- Update, delete, and state-transition SQL must include the expected version in
  its predicate. Zero affected rows map to the shared version-conflict error.
- Mutation pre-reads must load authoritative rows without remote presentation
  enrichment. For create/update responses, read the resulting row inside the
  write transaction before commit so a failed read rolls back rather than
  reporting failure after a successful commit. Resolve display names only
  after commit; an unavailable display-only dependency must not turn a
  committed mutation into an error. Use the existing stable-ID display fallback
  and test both dependency outage and transactional read failure.
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
- Tree contracts must also bound maximum depth at writes, reads, and provider
  validation. A node-count limit alone does not prevent deep-chain CPU,
  allocation, JSON nesting, or database-recursion exhaustion. Reject a write
  that would exceed the depth limit in the same transaction; do not create
  data that the read/serialization path cannot return.
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
- After editing an `integration`-tagged Go test, locally compile its own package
  with `go test -tags=integration -run '^$' ./path/to/package` without running
  any integration case. Every package with such a test must be included in both
  `make test-integration` and `make ci-test-integration`; the policy gate scans
  tagged test files to detect omissions. Container-backed execution still
  belongs exclusively to GitHub CI.
- Do not push incremental changes merely to trigger CI. Run the full CI suite
  after a substantial feature is complete; during normal local work, run only
  relevant unit tests.
- After changing a Go dependency, run `go mod tidy` and `make modules-check`
  before committing. The module check is read-only and mirrors the CI gate.
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

At dependency-injection and provider-registration boundaries, an interface
holding a typed nil pointer is still non-nil. Validate required interface
dependencies with the shared `internal/dependency.IsNil` helper before calling
their methods or publishing a registry. Add a unit regression for each new
interface registry so miswiring fails at startup instead of during a request.

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
