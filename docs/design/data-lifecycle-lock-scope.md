# Data lifecycle maintenance lock scope

| Concern | Decision and rationale |
| --- | --- |
| Contract | Internal scheduled `Maintain` command; no HTTP/gRPC request, response, pagination, sort, or filters. Errors propagate to the existing worker and metrics. |
| Authentication | Internal-only background command under an explicit system principal; no public route or user credential is accepted. |
| Authorization | Fx-owned lifecycle worker invokes maintenance for its configured service storage only. No tenant-level operation is exposed. |
| Operation log | No per-row operation log: partition retention and bounded cleanup are scheduled infrastructure operations observed through structured logs and infrastructure metrics. |
| Security log | No user security event is emitted; this command does not authenticate or change privileges. |
| Cache | None. Redis is used only for a disposable coordination lease; the database remains authoritative. |
| Distributed lock | Redis key includes dialect, service, database name, and schema in addition to the existing environment/service Redis prefix. Replicas of one target share a lock; unrelated targets do not suppress one another's retention runs. The configured TTL and shared lease-renewal context remain unchanged. PostgreSQL/Kingbase retain the database advisory transaction lock as the final local serialization boundary. Redis errors fail the maintenance attempt rather than silently running unlocked. |
| Optimistic lock | Not applicable to partition DDL or bounded retention batches; database transaction, advisory lock, and bounded statements govern concurrency. |
| Audit | Existing cleanup uses the system actor and `database.Transactor` for audited updates; this lock change makes no row mutation itself. |
| Presentation | No frontend response or timestamp conversion. Logs and metrics retain bounded labels and do not expose configuration secrets. |
| Tests | Unit regression checks that one storage target's replicas use the same key while service, database, schema, and dialect changes produce distinct keys. Existing tests cover bounded system Context, lock contention, and maintenance queries; tagged integration tests remain CI-owned. |
| Shared capability and dictionary | Reuses SDK-backed `cache.Locker` and `cache.TryWithLock`; no local lock algorithm. Operational maintenance is not a data-dictionary provider. |
