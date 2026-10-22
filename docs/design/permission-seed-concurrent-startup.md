# Built-in permission seed during concurrent startup

| Concern | Decision and rationale |
| --- | --- |
| Contract | `Seed` takes a reviewed built-in permission definition and returns the record plus `changed`; a concurrent identical winner returns `changed=false`, while divergent data returns `ErrConflict`. This is not a public CRUD endpoint. |
| Authentication | Internal startup command with an explicit system principal. |
| Authorization | Only reviewed code-owned seed definitions; no client-controlled resource/action registration. |
| Operation log | A committed seed uses the transactional `permission.seed` record. An equivalent concurrent winner is a no-op and emits no misleading failed-attempt record; a divergent winner remains a failed operation. |
| Security log | The existing permission-change recorder captures successful writes and genuine failures, but not an equivalent concurrent no-op. |
| Cache | None; database is authoritative for startup seeds. |
| Distributed lock | None; startup across instances must converge using database uniqueness and exact-record verification, independent of Redis. |
| Optimistic lock | Ordinary permission updates use versions; the insert race is resolved only when the committed winner has the exact requested stable ID and fields. |
| Audit | Database audit trigger and system principal maintain audit fields. |
| Presentation | Existing actor-name presentation applies; an unavailable identity display service falls back to actor IDs. |
| Tests | Unit tests cover identical winner, divergent winner, and manual-record conflict. Existing integration migration/seed tests remain CI-owned. |
| Shared capability | Uses the existing stable-ID, transactor, database unique-violation, and log components; no new shared primitive needed. |
| Data dictionary | Not a provider: permissions are security configuration, not a bounded selection dictionary. |

The recovery read is deliberately limited to insert unique conflicts. It never
converts a different row, deleted row, manual row, or database failure into a
successful seed result.
