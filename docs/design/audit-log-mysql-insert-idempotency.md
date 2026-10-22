# MySQL audit-log insert idempotency

| Concern | Decision and rationale |
| --- | --- |
| Contract | Operation and security log consumers persist one event by its stable event ID. A duplicate ID is an idempotent success; any other database failure is returned so the durable queue can retry or dead-letter it. No HTTP/gRPC contract or pagination changes. |
| Authentication | Internal queue consumer only. Events are validated against the canonical envelope and do not accept anonymous public writes directly. |
| Authorization | No user route is added. The consumer writes its owned tables under the event actor/system transaction context. |
| Operation log | This is the operation-log storage path itself; it must not recursively record another operation event. |
| Security log | This is the independent security-log storage path; it must not recursively record another security event. Security-critical producer enqueue behavior remains unchanged. |
| Cache | None. Audit events are durable facts, not reconstructable cache values. |
| Distributed lock | None. The database primary key on event ID is the final concurrency and duplicate-delivery boundary. |
| Optimistic lock | Not applicable to append-only inserts. Duplicate delivery must not update the existing row or increment its audit version. |
| Audit | Existing insert triggers maintain mandatory audit columns. Plain MySQL `INSERT` reports truncation/constraint errors instead of allowing `INSERT IGNORE` to coerce values or turn failures into warnings. Only MySQL duplicate-key error 1062 is accepted because each log table currently has exactly one unique key, its event-ID primary key; adding another unique key requires this decision to be revisited. PostgreSQL/Kingbase retain their explicit `(id, occurred_at)` conflict target. |
| Presentation | Stored log fields, timezone rendering, actor/resource snapshots, masking, and response models are unchanged. |
| Tests | Unit tests for both consumers require plain `INSERT`, duplicate replay success, MySQL data-too-long failure, and ordinary database failure. The MySQL Testcontainers lifecycle checks both log tables' duplicate-key and overlong-value errors, plus connector enforcement when a DSN explicitly requests an empty SQL mode. Direct consumer duplicate delivery against MySQL remains a separate integration gap. Integration suites remain CI-only locally. |
| Shared capability and dictionary | Reuses `database.IsUniqueViolation`, shared transaction actor, event bus retry semantics, and log redaction. Log events are sensitive/high-volume history, not data-dictionary providers. |

MySQL log retention remains a separate unresolved SOP conflict: the current bounded physical purge and its trigger exception predate the present rule forbidding physical deletion from audited tables. This insert fix does not change retention policy.

MySQL documents that `INSERT IGNORE` can coerce invalid values or demote write errors to warnings; see the [MySQL INSERT reference](https://dev.mysql.com/doc/refman/8.0/en/insert.html) and [MySQL IGNORE behavior](https://dev.mysql.com/blog-archive/improvements-to-the-mysql-ignore-implementation/). The shared MySQL connector now appends `STRICT_TRANS_TABLES` to each connection's configured or inherited session SQL mode, retaining other modes while ensuring invalid values produce errors. The CI Testcontainers lifecycle checks the resulting session mode.
