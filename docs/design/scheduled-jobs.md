# Scheduled job definitions

Scheduled jobs are platform-owned definitions. A definition selects a handler
that was registered by Go code; it cannot contain an arbitrary URL, SQL
statement, shell command, or gRPC method name. This keeps configuration from
becoming a remote-code-execution surface and gives every executable handler a
testable owner.

| Concern | Decision |
| --- | --- |
| Contract | `POST /api/v1/scheduled-jobs/{create,get,page,update,delete,trigger}`, handler discovery, and execution-history get/page use the shared envelope. Pages support typed filters and allowlisted stable sorting. |
| Authentication | JWT only. |
| Authorization | Platform resource `scheduled-job`; actions `create/read/list/update/delete/execute`. Data permission is not applicable because definitions are platform-scoped. |
| Operation log | Record every create, update, delete and manual execution as `platform.scheduled-job.*`; reads are not logged. Payloads contain configuration only and never credentials. |
| Security log | None. Changing a schedule is an administrative operation but not an authentication/session/credential event. |
| Cache | The cron runner is an in-memory derived snapshot. A local post-commit notification refreshes immediately and bounded database polling recovers lost notifications. The database remains authoritative. |
| Distributed lock | CRUD does not use Redis. Execution continues to use the existing smallest-key lock `cron:<job-code>` with lease renewal. |
| Optimistic lock | Update and delete require `version`; zero affected rows is a conflict. |
| Audit | All three database variants contain mandatory fields. PostgreSQL/Kingbase use `app_enable_audit`; MySQL uses BI/BU/BD audit triggers. Writes run through `database.Transactor`. |
| Presentation | RFC 3339 timestamps with `Asia/Shanghai` presentation and actor display names. Definition durations are seconds; execution duration is milliseconds. |
| Tests | Unit tests cover normalization, cron/timezone/handler validation, page/sort allowlists, atomic snapshot replacement, lock-protected execution, and history persistence. Integration behavior remains under the `integration` build tag in CI. |
| Shared capability | Reuses pagination, transactor, operation log, actor presentation, PBAC registry and the existing cron/lock runner. |

Definitions are operational configuration rather than a bounded business
dictionary, so this module does not register a data-dictionary provider.

Every run stores bounded snapshots of the task identity, trigger source,
status, duration, Request ID and Trace ID. Payloads and raw technical errors are
not copied into history. Metrics label executions by registered handler key,
never by administrator-defined job code.
