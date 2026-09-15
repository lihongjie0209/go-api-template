# Go API template SOP audit

This ledger applies only to the repository rooted at
`/root/code/go-api-template`. Evidence from generated services, platform
services, sibling repositories, or historical green builds does not establish
compliance for this repository.

The source of truth is `AGENTS.md`. A feature is `verified` only after its
applicable contract, authentication, authorization, logging, cache, lock,
optimistic-lock, audit, presentation, observability, unit-test, integration-test
and shared-component decisions have direct evidence in this repository.

Status values:

- `pending`: not yet inspected against the complete SOP.
- `in_progress`: inspected, but evidence is incomplete or gaps remain.
- `verified`: every applicable SOP concern has direct implementation and test evidence.
- `not_applicable`: reviewed infrastructure concern with a recorded rationale.

## Audit order and status

| Capability | Status | Evidence and open work |
| --- | --- | --- |
| Configuration profiles and environment context | verified | Base/profile/environment/flag precedence, current-directory discovery, defaults, runtime profile/file reporting and HTTP/gRPC context propagation are covered. Shipped profiles and production fail-fast rules have direct regression tests; the full local quality gate passes. |
| Dependency injection and process lifecycle | verified | Uber Fx graph construction plus real start/stop is tested. Database/Redis/NATS/outbound/JWKS/tracing/logging resources have reverse-order cleanup; HTTP, gRPC and cron honor the shutdown deadline. Background workers and blocking event consumers now use a shared cancel-and-join lifecycle primitive. |
| Logging and build metadata | verified | slog JSON/text output is mirrored to stdout and a bounded lumberjack file sink. Service/environment/build identity and context Request/Trace/Span IDs are correlated without duplicate keys. API/migration binaries and OCI images receive consistent Git metadata. |
| Database connections, transactions and migrations | in_progress | Driver/schema/timezone/pool handling, per-service migration history, concurrent schema bootstrap, audit DDL and transactional actor propagation are reviewed. MySQL actor leakage was fixed and unit-covered; the updated PostgreSQL/MySQL Testcontainers suite is awaiting CI evidence. |
| HTTP contract and middleware | pending | POST envelope, request ID, timeout, JSON, CORS, headers, trusted proxies and error codes require review. |
| Authentication, JWT, JWKS and sessions | pending | Full identity/security lifecycle review required. |
| Database route authorization | pending | Route discovery, UUIDv5 identity, CEL, cache refresh and fail-closed behavior require review. |
| Rate limiting | pending | IP/API/user/login dimensions and Redis failure policy require review. |
| Distributed cache | pending | Shared abstraction, key bounds, TTL, invalidation and observability require review. |
| Distributed locks | pending | Lock granularity, renewal, context cancellation and ownership-loss behavior require review. |
| Idempotency | pending | HTTP/gRPC state machine, fingerprints, leases and replay bounds require review. |
| Event bus and durable delivery | pending | JetStream delivery and transactional-outbox boundaries require review. |
| Operation logs and frontend events | pending | Async persistence, sanitization, query isolation and retention require review. |
| Security logs | pending | Independent chain, pre-auth identity hashing, fail-closed and redaction require review. |
| Object storage and files | pending | S3/OSS abstraction, upload/download metadata and deletion repair require review. |
| Health checks | pending | HTTP/gRPC liveness/readiness and dependency timeout isolation require review. |
| Observability and diagnostics | pending | Metrics, traces, bounded labels, pprof and correlation require review. |
| Scheduler and data lifecycle | pending | System principals, cron metrics, partition retention and archival require review. |
| Outbound HTTP/gRPC clients | pending | Authentication, mTLS, timeout, retry, breaker, discovery and telemetry require review. |
| Users and service accounts | pending | Contracts, global identity, credentials, locks, logs and presentation require review. |
| Tenants, memberships and context | pending | Tenant SQL isolation, lifecycle, context switching and invariants require review. |
| Departments and tree contract | pending | Tree safety, filtering, display and lock scope require review. |
| Tenant roles and authorization | pending | Delegation ceilings, role grants, isolation and permission presentation require review. |
| Permission definitions | pending | Stable identifiers, tree contract and reference safety require review. |
| Menus | pending | Stable identifiers, tree filtering, permission gating and caching require review. |
| Platform configuration | pending | Public boundary, value typing, secret rejection and cache invalidation require review. |
| OpenAPI and protobuf contracts | pending | Route completeness, error documentation and generated drift require review. |
| Scaffold CLI | pending | Cobra/Viper precedence, AST rewriting, generated profiles and safety require review. |
| Docker Compose and Kubernetes | pending | Local dependencies, migrations, probes and production hardening require review. |
| Unit/integration tests and CI | pending | Test isolation, Testcontainers policy and all required gates require review. |

## Configuration profile audit decisions

| Concern | Current decision and evidence |
| --- | --- |
| Contract | `config.LoadWithProfile(path, explicitProfile)` is the single loader for API and migration binaries. An omitted path discovers `config.*` in the current directory and then `./config`; an explicit missing or malformed file fails. |
| Precedence | Defaults are installed first, then the base file, sibling `config-{profile}` file, `APP_*` environment values, and finally the explicit `-env` profile. Existing tests prove profile-before-environment behavior, environment list decoding and explicit profile selection. |
| Runtime context | `Config.Runtime` exposes the normalized active profile and only the files actually loaded. HTTP and unary/stream gRPC middleware place that profile in request context, and the root logger and OTel resource carry it. |
| Validation/security | Configuration validates dependency relationships and production restrictions before Fx starts. Secret fields are blank in committed YAML. Production now fails closed without gRPC TLS, disabled reflection, JWKS-based authorization, an asymmetric JWT signing key, and the durable event/operation/security-log chain. |
| Logs/cache/locks/audit | Not applicable to the loader itself: it performs no business mutation and owns no persistent or cached state. It supplies bounded settings to those components. |
| Presentation | Profile names are normalized to lowercase and validated; loaded paths remain diagnostic runtime metadata and are not exposed as business data. |
| Tests | Unit tests cover file/environment/profile precedence, current-directory discovery, defaults, shipped development/test/production profiles, asymmetric JWT pairing, PSK length, migration dependencies, authorization dependencies, outbound credential transport, lifecycle bounds, production fail-closed rules, and HTTP/unary/stream gRPC profile propagation. `go test`, race tests, vet, lint, Swagger/Proto drift, Compose validation and the integration-test policy gate pass. |
| Shared capability | Uses Viper and mapstructure rather than a local parser. Both API and migration commands reuse the same loader. |

## Dependency injection and lifecycle audit decisions

| Concern | Current decision and evidence |
| --- | --- |
| Dependency graph | `app.New` composes named Fx modules and constructor injection; no service locator or mutable global container is used. The graph test now performs a real `Start` and `Stop`, rather than checking only `App.Err`. |
| Startup ordering | Automatic migration is invoked before database-backed transports are constructed. HTTP/gRPC listeners are bound in lifecycle hooks, so bind failures are returned to Fx. Route synchronization and initial policy compilation finish before serving traffic. |
| Owned resources | Logger, SQL pool, Redis client, NATS connection, outbound gRPC connections, JWKS verifier and trace provider register Fx stop hooks. Fx reverse hook order stops consumers and servers before their dependencies. Constructor failures close resources already created within that constructor. |
| Background work | File deletion, data lifecycle maintenance and route-policy refresh use `background.Worker`; cancellation is followed by a bounded join using the Fx stop context. HTTP starts its policy worker only after the listener binds, preventing a failed start from leaking the worker. |
| Event consumers | The shared `eventbus.RegisterConsumer` adapter runs the SDK's blocking consumer outside `OnStart`, reports unexpected exits, and cancels and joins it during `OnStop`. This fixes the prior startup deadlock when operation/security logging was enabled. |
| Shutdown | HTTP uses `Server.Shutdown`, gRPC attempts `GracefulStop` and falls back to `Stop` on deadline, cron waits for running jobs, and all managed workers honor the configured Fx stop timeout. |
| Tests | Unit tests prove managed-worker cancellation, deadline behavior, stop-before-start safety, asynchronous consumer start and joined shutdown. App tests exercise the complete Fx graph through actual start and stop. Targeted unit, race and vet gates pass. |
| SOP applicability | Lifecycle infrastructure performs no business mutation and owns no API presentation, authorization, cache or audit-record decision. Its responsibility is deterministic construction, failure propagation and teardown. |

## Logging and build metadata audit decisions

| Concern | Current decision and evidence |
| --- | --- |
| Structured logging | The shared `logging.New` creates `slog` JSON or text output and writes to stdout plus a lumberjack-managed file. Configuration now rejects unsupported levels/formats, blank paths and non-positive or unbounded rotation limits before Fx starts. |
| Stable process fields | The root logger binds service, active environment, version, full commit and UTC build time once. All injected child loggers inherit them. |
| Request correlation | The context handler adds Request ID and valid OpenTelemetry Trace/Span IDs. It detects both per-record and pre-bound correlation fields, avoiding duplicate JSON keys while preserving explicit values. HTTP/gRPC access, error and recovery logs use context-aware logging. |
| Sensitive data | Infrastructure request logs contain method/path/status/duration/client IP, not request bodies or authorization headers. Operation/security payloads use their separately audited shared redaction/digest paths; credential material is not configured as a root logger attribute. |
| File lifecycle | The logger creates its parent directory with restricted directory permissions and its rotator is closed by an Fx hook after dependent components stop. Compose uses a named log volume and Kubernetes uses a writable dedicated `emptyDir` while retaining a read-only root filesystem. |
| Build metadata | Make and Docker builds inject the same Version/Commit/BuildTime variables with `-trimpath`; API and migration binaries expose them through `-version`, the API exposes runtime start/uptime, and OCI labels carry version/revision/created values. CI supplies and asserts exact Git metadata before image publication. |
| Tests | Tests cover structured file output and close, absent/present context correlation, bound-field de-duplication and runtime build information. An audited local build with fixed values produced exact matching `api -version` and `migrate -version` output; unit, race and vet gates pass. |
| SOP applicability | Logging/build infrastructure does not mutate domain state and therefore has no business authorization, cache, lock, optimistic-lock or audit-row decision. It must avoid secrets and preserve correlation for the modules that do. |

## Database, transaction and migration audit decisions

| Concern | Current decision and evidence |
| --- | --- |
| Dialects and database selection | Runtime connections support PostgreSQL and Kingbase through pgx and MySQL through go-sql-driver/mysql. `database.name` overrides the DSN database. PostgreSQL/Kingbase set `search_path`; clients and sessions use Asia/Shanghai / `+08:00`. |
| Pool safety | Enabled databases require a bounded positive open-connection count, a non-negative idle count no greater than open connections, positive lifetime/idle limits and a positive ping timeout. Opening performs a bounded ping and closes on failure; Fx closes the pool on shutdown. |
| Transactions and audit actor | All domain writes use `Transactor.Within`, which rejects a missing principal. PostgreSQL/Kingbase use transaction-local `set_config`. MySQL uses its connection variable only while the transaction is active and now clears it before commit and before rollback, including cancellation and panic paths, preventing pooled-connection identity leakage. |
| Audit schema | A migration contract test scans all three dialects and requires every created table, including associations, to contain the seven canonical audit/version/soft-delete fields. PostgreSQL/Kingbase require `app_enable_audit`; MySQL requires insert/update triggers and delete guards except append-only log retention tables. |
| Trigger behavior | PostgreSQL/Kingbase fail writes without `app.actor_id`, preserve creation metadata, own timestamps and version increments, maintain delete metadata and reject physical delete. MySQL audited columns are non-null and trigger-owned; operation/security logs deliberately permit physical deletion only for bounded retention maintenance. |
| Migration isolation | golang-migrate receives the generated service-specific history table. PostgreSQL/Kingbase receive the configured database and search path; MySQL receives the configured database. Optional schema creation is identifier-validated and serialized by a database advisory transaction lock. Unknown directions are rejected before opening a database. |
| Startup and CLI | The same migration runner is used by Fx automatic startup migration, the dedicated versioned migrate binary, Compose migration jobs and Kubernetes migration Job. Startup failure prevents transports from serving. |
| Tests | Unit tests cover DSN/database/schema options, timezone parameters, schema bootstrap locking, audit DDL, actor requirements, commit/rollback behavior, MySQL actor cleanup and migration direction. The integration suite exercises concurrent up, service-owned history, CRUD/transactions, triggers, optimistic locking, retention and full down on PostgreSQL and MySQL. Its updated CI result is still required before this capability becomes `verified`. |
