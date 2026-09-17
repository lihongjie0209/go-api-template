# Frontend navigation telemetry and menu usage

## Interface decisions

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/operation-logs/frontend/record` records one bounded `menu_view` or `button_click`; `POST /api/v1/me/navigation-usage` accepts `{}` and returns at most 1000 `{application_id,menu_id,click_count,last_clicked_at}` rows from the last 90 days. Both use the shared response envelope. |
| Authentication | JWT only. Telemetry is never accepted anonymously because actor and tenant context are authoritative event dimensions. |
| Authorization | Recording uses principal-scoped `frontend.telemetry:record`, separated from privileged operation-log reading. Usage uses tenant-scoped `navigation.current:read` and derives actor/tenant from the principal. |
| Operation log | The telemetry record is itself persisted asynchronously through `operationlog.Recorder`. The usage read emits no additional operation log because it is a non-sensitive self query and logging it would pollute usage. |
| Security log | None. Menu views and ordinary button clicks are operational product telemetry, not authentication or privilege security events. |
| Cache | None initially. The 90-day aggregate is user-specific and eventually consistent with asynchronous ingestion; adding a short cache can follow measured load. |
| Distributed lock | None. Events are append-only and aggregation is read-only. Duplicate browser delivery is acceptable telemetry semantics and never changes authoritative business state. |
| Optimistic lock | Not applicable because neither interface updates an existing business row. |
| Audit | The operation-log consumer maintains mandatory audit fields. Query scope uses the trusted principal ID and exact tenant ID, including an empty platform context. |
| Presentation | `last_clicked_at` is RFC 3339 with an explicit Asia/Shanghai offset. Application and menu IDs are consumed with names already owned by the navigation response. |
| Tests | Unit tests cover exact actor/tenant SQL scope, event validation, best-effort frontend delivery, success/failure button events, and router menu events. Integration coverage remains service-local under the existing operation-log integration suite. |
| Shared capability | Reuses the operation-log outbox, common request client, router metadata, PBAC descriptors, and standard resource components. The frontend telemetry helper becomes the shared entry point; components must not call the endpoint directly. |
| Data dictionary | Not exposed. Usage records are actor-specific, time-varying telemetry rather than bounded reference data. |

The usage query intentionally uses a rolling 90-day window so PostgreSQL and
Kingbase can prune historical partitions and so deleted navigation definitions
do not create an unbounded response. Results are ordered by most recent use and
then stable application/menu IDs.
