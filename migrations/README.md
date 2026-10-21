# Migrations

Database-specific migrations live under `mysql`, `postgres`, and `kingbase`. Set `migration.path` to the matching directory, for example `migrations/postgres`. Review indexes, collation and online-DDL impact against production data before deployment.

When `migration.create_schema` is enabled for PostgreSQL/Kingbase, concurrent
replicas serialize schema creation with a transaction-scoped advisory lock. The
bootstrap transaction now has a 30-second deadline; a stuck lock or database
call fails startup instead of waiting indefinitely. This bound covers schema
creation only, not the subsequent `golang-migrate` up/down execution. Configure
database-side statement and lock timeouts separately for migration DDL.
