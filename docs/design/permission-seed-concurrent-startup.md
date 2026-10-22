# Built-in permission seed during concurrent startup

| Concern | Decision and rationale |
| --- | --- |
| Contract | `Seed` takes a reviewed built-in permission definition and returns the record plus `changed`; a concurrent identical winner returns `changed=false`, while divergent data returns `ErrConflict`. The current row is read and compared inside each transaction attempt. This is not a public CRUD endpoint. |
| Authentication | Internal startup command with an explicit system principal. |
| Authorization | Only reviewed code-owned seed definitions; no client-controlled resource/action registration. |
| Operation log | A committed seed uses the transactional `permission.seed` record. An equivalent concurrent winner is a no-op and emits no misleading failed-attempt record; a divergent winner remains a failed operation. |
| Security log | The existing permission-change recorder captures successful writes and genuine failures, but not an equivalent concurrent no-op. |
| Cache | None; database is authoritative for startup seeds. |
| Distributed lock | None; startup across instances converges using database uniqueness, exact-record verification, and bounded retry of driver-reported transaction aborts, independent of Redis. |
| Optimistic lock | Seed updates require the version read before mutation in `WHERE id=? AND is_system=true AND version=?`; zero affected rows is a conflict, so an older deployment cannot silently overwrite a newer seed. The insert race is resolved only when the committed winner has the exact requested stable ID and fields. |
| Audit | Database audit trigger and system principal maintain audit fields. |
| Presentation | Existing actor-name presentation applies; an unavailable identity display service falls back to actor IDs. |
| Tests | Unit tests cover identical winner, divergent winner, manual-record conflict, audit side effects, stale-version update, serialization retry, and recovery-read failure. CI Testcontainers tests race four instances against one database for PostgreSQL and MySQL. |
| Shared capability | Uses the existing stable-ID, transactor, database unique-violation, and log components; shared database error classification now recognizes retryable PostgreSQL/Kingbase serialization/deadlock and MySQL deadlock errors. |
| Data dictionary | Not a provider: permissions are security configuration, not a bounded selection dictionary. |

The recovery read runs after a unique violation, transaction abort, or seed
version conflict, before any failed-attempt event is emitted. Each retry rereads
the current seed inside its own transaction before deciding whether to write.
Recovery never converts
a different row, deleted row, manual row, or database failure into a successful
seed result. Transaction-abort retries are limited to three attempts and respect
the caller context; ordinary permission mutations do not gain automatic retry.
