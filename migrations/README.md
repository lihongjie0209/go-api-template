# Migrations

Database-specific migrations live under `mysql`, `postgres`, and `kingbase`. Set `migration.path` to the matching directory, for example `migrations/postgres`. Review indexes, collation and online-DDL impact against production data before deployment.

When `migration.create_schema` is enabled for PostgreSQL/Kingbase, concurrent
replicas serialize schema creation with a transaction-scoped advisory lock. The
bootstrap transaction now has a 30-second deadline; a stuck lock or database
call fails startup instead of waiting indefinitely.

PostgreSQL/Kingbase migration connections also receive a server-side
`lock_timeout` from `migration.lock_timeout` (default 30 seconds, accepted range
1 second to 5 minutes). It applies while `golang-migrate` waits for its own
session-level advisory lock, including during migrator construction, and for
locks needed by migration statements. The migration configuration overrides a
`lock_timeout` query parameter in the URL and rejects a conflicting `options`
setting. This is a lock-wait bound, not a total migration duration or a
statement-execution bound. Set `x-statement-timeout` in the migration URL when
individual DDL statements also need a bounded execution time. MySQL retains
the migration driver's bounded `GET_LOCK` behavior.
