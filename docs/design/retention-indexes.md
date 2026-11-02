# Retention cleanup indexes

The reviewed lifecycle worker deletes expired MySQL operation and security
log rows in bounded batches ordered by `(occurred_at, id)`. Existing indexes
begin with tenant, actor, subject, or event fields, so they do not serve this
global retention scan. Refresh-token lineage cleanup logically deletes expired
rows, but its original `(expires_at, token_hash)` index keeps all prior deleted
rows ahead of the next active batch. Migration `000053` adds the log indexes and
replaces the lineage index with `(deleted_at, expires_at, token_hash)`.
Outbox cleanup previously combined published and dead rows with `OR` and sorted
by creation time, preventing a useful ordered range scan. All three dialects
now run separate bounded paths by publication and dead-letter time. MySQL uses
`(deleted_at, published_at, id)` and `(deleted_at, dead_at, id)` indexes;
PostgreSQL/Kingbase use partial `(published_at, id)` and `(dead_at, id)` indexes
on non-deleted rows. PostgreSQL/Kingbase retain monthly log partitions and
already have a partial index on active lineage.

| Concern | Decision and rationale |
| --- | --- |
| Contract | No public API changes. Existing retention cutoffs and batch size remain authoritative. Published and dead outbox classes each receive at most the configured batch count per maintenance run, so neither can starve the other. |
| Authentication and authorization | Internal scheduled lifecycle command with the existing system principal and distributed lease. No business route is added. |
| Operation and security logs | The lifecycle command is the reviewed physical-purge exception for expired MySQL audit rows. It does not recursively log its own purge. |
| Cache and lock | No cache. The existing Redis lease serializes maintenance instances; the database remains the retention authority. |
| Optimistic lock and audit | Append-only log writes, the bounded physical-purge exception, and audited lineage soft deletion remain unchanged. No business update/delete path is added. |
| Presentation | No response model or timezone change. The cutoff remains the start of a month in +08. |
| Tests | Unit tests verify bounded purge/lineage cleanup SQL, independent outbox class budgets for MySQL/PostgreSQL, and cutoff behavior. The service-local PostgreSQL/MySQL Testcontainers suite checks the dialect indexes, clears two expired rows from each outbox class with one batch per class, retains pending/recent rows, and executes down migration. CI execution is pending. |
| Shared capability and dictionary | Reuses the lifecycle manager and migration runner. Sensitive, high-volume logs are unsuitable dictionary data. |
