# Go Web API Template

Production-oriented starter using Gin, Uber Fx, Viper, slog + lumberjack, sqlx, Redis, JWT, robfig/cron, and golang-migrate. Fork it or change the module path before starting a separate project.

## Quick start

```bash
cp config/config.yaml config/config.local.yaml
export APP_JWT_SECRET='replace-with-at-least-32-random-bytes'
export APP_AUTH_CLIENT_ID='local-client'
export APP_AUTH_CLIENT_SECRET='local-secret'
go run ./cmd/api -config config/config.local.yaml
```

For the complete local stack (API, MySQL, PostgreSQL, Redis and automatic migrations):

```bash
make dev-up
# API http://127.0.0.1:8080, gRPC 127.0.0.1:9090
# MySQL 127.0.0.1:3306, PostgreSQL 127.0.0.1:5432, Redis 127.0.0.1:6379
make dev-logs
make dev-down
```

If the default Go module proxy is unavailable on your network, override it only for the build, for example `GOPROXY=https://goproxy.cn,direct make dev-up`.

Release metadata is injected automatically at build time. `make build`, `make docker-build`, and `make dev-up` embed the Git-derived version, full commit SHA, and UTC build time into the API binary. Docker images also expose the same values as OCI labels. Override them when needed with `VERSION=v1.2.3 COMMIT=<sha> BUILD_TIME=<RFC3339>`.

```bash
make docker-build
docker inspect go-api-template:$(git describe --tags --always --dirty) \
  --format '{{json .Config.Labels}}'
./bin/api -version
curl -sS -X POST http://127.0.0.1:8080/api/v1/version
```

The Compose `compose` profile runs the API against PostgreSQL while also migrating MySQL for compatibility work. The development stack intentionally does not start Prometheus, Grafana, Jaeger or an OTel Collector.

All nested config keys can be overridden with `APP_` environment variables: `database.dsn` becomes `APP_DATABASE_DSN`. Environment values override the YAML file. Keep secrets out of YAML and source control.

Environment profiles work like Spring Boot: load `config.yaml`, then an optional sibling `config-{env}.yaml`, then apply environment variables. Select the profile with `-env production` or `APP_ENV=production`; the flag has the highest priority. The active profile and loaded file list are available through `config.Config.Runtime`, while the profile is also placed in HTTP/gRPC contexts through `environment.FromContext` and attached to every structured log entry.

```bash
curl -sS -X POST http://127.0.0.1:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"client_id":"local-client","client_secret":"local-secret"}'
```

Business endpoints use POST with JSON; operational probes also expose GET for Docker/Kubernetes. Responses always use:

```json
{"code":0,"message":"success","body":{}}
```

## Error codes

| Range | Meaning | Examples |
|---|---|---|
| `0` | success | `0` |
| `10000-19999` | protocol/input/common | invalid argument `10001`, not found `10004`, throttled `10029` |
| `20000-29999` | authentication/authorization | unauthorized `20001`, forbidden `20003` |
| `30000-39999` | business rules | conflict `30009` |
| `50000-59999` | server/infrastructure | internal `50000`, dependency unavailable `50003` |

HTTP status codes remain semantically correct; clients should use `code` for stable application behavior. Technical errors are logged and never returned to clients.

## Database

Set `APP_DATABASE_ENABLED=true`, `APP_DATABASE_TYPE`, and `APP_DATABASE_DSN`.

- MySQL: type `mysql`, DSN `user:pass@tcp(127.0.0.1:3306)/app?parseTime=true`
- PostgreSQL: type `postgres`, DSN `postgres://user:pass@127.0.0.1:5432/app?sslmode=disable`
- KingbaseES: type `kingbase`, using its PostgreSQL-compatible wire protocol through pgx; use a Kingbase-compatible PostgreSQL DSN. If your installation mandates official Gokb, add its private module import and map `kingbase` to driver name `kingbase` in `internal/database/database.go`.

The official Kingbase documentation describes Gokb as a pure-Go `database/sql` driver registered as `kingbase`, but distribution commonly accompanies the product rather than a stable public Go module.

## Redis lock and scheduled jobs

Distributed locking is implemented with [`go-redsync/redsync/v4`](https://github.com/go-redsync/redsync). The local `cache.Locker` adapter provides non-blocking `TryLock`, context-aware retrying `Lock`, ownership-safe `Unlock`, explicit `Extend`, and the lock validity deadline through `Until`. The sample six-field cron job demonstrates cross-instance locking.

Redsync does not start a hidden renewal goroutine. Long-running jobs must call `Extend` before `Until`, or stop work when extension fails. This keeps goroutine ownership and lock-loss behavior explicit.

## HTTP operations and security

- `GET|POST /live`: process liveness
- `GET|POST /ready`: database and Redis readiness with independent timeouts
- `GET /metrics`: Prometheus metrics when enabled
- `POST /api/v1/version`: version, commit, build time, start time and uptime
- `POST /api/v1/users/create|get|list|update|delete`: JWT-protected CRUD example
- `GET /swagger/index.html`: generated Swagger UI when enabled

Every request accepts or generates `X-Request-ID`; it is returned in the response header and JSON envelope and correlated with OpenTelemetry trace/span IDs in logs. Request deadlines are propagated through `Request.Context`, so context-aware SQL and Redis calls stop after client cancellation or timeout.

Redis-backed GCRA limits are configurable for IP, API route, authenticated user and login brute-force protection. Set `APP_REDIS_ENABLED=true` and `APP_RATE_LIMIT_ENABLED=true` to enable them. `rate_limit.fail_open` controls behavior when Redis is unavailable and defaults to secure fail-closed mode.

Configure `http.trusted_proxies` explicitly before trusting forwarding headers. CORS is deny-by-default, JSON bodies require `application/json`, and baseline browser security headers are enabled globally.

JWT bypass and PSK policies are configuration-driven. `auth.skip_http_paths` and `auth.skip_grpc_methods` bypass authentication; `auth.psk.http_paths` and `auth.psk.grpc_methods` require `Authorization: PSK <key>` and take precedence over bypass/JWT rules. Patterns use Go `path.Match`: `*` and `?` are supported but do not cross `/`. Enable PSK with `APP_AUTH_PSK_ENABLED=true` and inject a key of at least 32 bytes through `APP_AUTH_PSK_KEY`; never store a production key in YAML.

## OpenAPI and observability

Generate the checked-in Swagger contract with `make swagger`. CI regenerates `docs/` and fails when it differs from the committed output. JWT-protected operations declare the `Bearer` security scheme. In production, Swagger can only be enabled when `swagger.require_auth=true`.

Prometheus exports bounded-cardinality HTTP latency/status metrics, database pool metrics, Redis pool metrics, Go/process metrics and Cron execution metrics. Enable OTLP/HTTP tracing with:

```bash
export APP_OBSERVABILITY_TRACING_ENABLED=true
export APP_OBSERVABILITY_TRACING_ENDPOINT=http://otel-collector:4318
export APP_OBSERVABILITY_TRACING_SAMPLE_RATIO=0.1
```

pprof is disabled by default. Enabling it requires an independent Bearer token of at least 32 bytes:

```bash
export APP_OBSERVABILITY_PPROF_ENABLED=true
export APP_OBSERVABILITY_PPROF_TOKEN='replace-with-a-separate-32-byte-token'
curl -H "Authorization: Bearer $APP_OBSERVABILITY_PPROF_TOKEN" http://127.0.0.1:8080/debug/pprof/
```

## gRPC server and client

The gRPC server listens independently on `127.0.0.1:9090` and is managed by the same Fx lifecycle. The versioned contract is under `proto/hello/v1`; generated clients and servers are checked in under `gen/hello/v1`.

- `hello.v1.HelloService/Ping`: authenticated example RPC
- `hello.v1.UserService/*`: CRUD RPCs backed by the same service as HTTP
- `grpc.health.v1.Health/Check`: unauthenticated standard readiness check
- JWT is passed as `authorization: Bearer <token>` metadata
- `x-request-id` and W3C trace context propagate across HTTP-to-gRPC calls
- reflection is enabled for development and forbidden in production
- production configuration requires TLS; setting `client_ca_file` enables mTLS

Regenerate protobuf code with `make proto`. CI fails if generated stubs drift from the proto source. For outbound calls, use `internal/grpcclient.Dial`; it reuses the HTTP/2 connection, applies a default deadline, supports TLS/mTLS and refuses to send bearer credentials over plaintext unless explicitly allowed for development.

```go
conn, err := grpcclient.Dial(grpcclient.Config{
    Target:  "dns:///other-service:9090",
    Timeout: 5 * time.Second,
    Token:   accessToken,
    TLS:     grpcclient.TLSConfig{Enabled: true, ServerName: "other-service"},
})
if err != nil { /* handle */ }
defer conn.Close()

client := hellov1.NewHelloServiceClient(conn)
response, err := client.Ping(ctx, &hellov1.PingRequest{Message: "hello"})
```

For a PSK-protected upstream, set `PSK` instead of `Token`; the client sends `Authorization: PSK <key>`. Bearer and PSK are mutually exclusive and both require TLS by default.

## Migrations

The example user migrations are separated under `migrations/mysql`, `migrations/postgres`, and `migrations/kingbase`. Set `APP_MIGRATION_PATH` to the directory matching `APP_DATABASE_TYPE`, set `APP_MIGRATION_DATABASE_URL`, then run:

```bash
make migrate-up
make migrate-down
go run ./cmd/migrate -steps 1
go run ./cmd/migrate -steps -1
```

Use a `mysql://` URL for MySQL and `postgres://` for PostgreSQL/Kingbase. The sample schema and indexes still require review against real data volume and access patterns.

## User module example

`internal/user` demonstrates explicit SQL repositories, context propagation, a transaction boundary on create, optimistic locking through `version`, pagination, normalized input validation, Redis read-through caching, and a redsync lock keyed by a SHA-256 digest of the email. HTTP and gRPC are transport adapters over the same service.

```bash
curl -X POST http://127.0.0.1:8080/api/v1/users/create \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"Alice","email":"alice@example.com"}'
```

Updates and deletes must send the current `version`; stale writes return application code `30009`.

## Idempotency

Enable Redis-backed idempotency with `APP_IDEMPOTENCY_ENABLED=true`. Send an `Idempotency-Key` containing 8–128 safe ASCII characters on HTTP or `idempotency-key` metadata on gRPC. User creation stores `processing`, `completed`, or `failed` state; the same key and request replays the original business result, concurrent execution returns `30010`, and reusing a key with different input returns `30009`.

The Redis state transition uses Lua plus an owner token, so an expired worker cannot overwrite a newer owner. The database transaction commits before the completed result is published; unique database constraints remain the final integrity boundary.

## Outbound clients

Named HTTP and gRPC clients are created once by the Fx-managed outbound registry. Both support Bearer/PSK, TLS/mTLS, deadline propagation, bounded exponential retry, Sony gobreaker, Prometheus metrics and OpenTelemetry propagation. Credentials require TLS. POST/RPC retries are only enabled for configured safe gRPC methods or calls carrying an idempotency key.

```yaml
outbound:
  http:
    billing:
      base_url: https://billing.example.com
      timeout: 5s
      auth: {type: psk, token: ""} # inject per environment
      retry: {max_attempts: 3, initial_backoff: 100ms, max_backoff: 1s}
      breaker: {enabled: true, failure_threshold: 5, open_timeout: 30s}
      tls: {enabled: true, server_name: billing.example.com}
  grpc:
    inventory:
      target: dns:///inventory:9090
      timeout: 5s
      auth: {type: bearer, token: ""}
      retry:
        max_attempts: 3
        initial_backoff: 100ms
        max_backoff: 1s
        methods: [/inventory.v1.InventoryService/Get*]
      breaker: {enabled: true, failure_threshold: 5, open_timeout: 30s}
      tls: {enabled: true, server_name: inventory}
```

## Verification

```bash
go test ./...
go test -race ./...
make test-integration # requires Docker; runs Testcontainers MySQL/PostgreSQL/Redis and HTTP/gRPC E2E tests
golangci-lint run ./...
```

The GitHub Actions workflow runs unit/race/vet/generated-code checks and the integration suite as separate jobs.
