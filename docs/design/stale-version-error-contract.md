# Stale-version error contract

This correction applies to file deletion, user/profile update and deletion,
service-account update, secret rotation and deletion, tenant update/delete,
and tenant member status update/removal. These operations use
the shared POST + JSON response envelope. A compare-and-swap miss returns HTTP
409 with global code `30011` (`StaleVersion`); a uniqueness or reference
conflict remains code `30009` (`Conflict`). The frontend must refresh trusted
versions after `30011` and must never retry the write automatically.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Keep existing request/response models; distinguish stale versions (`30011`) from other conflicts (`30009`) so clients can recover correctly. |
| Authentication | Existing JWT requirements remain unchanged. |
| Authorization | Existing resource/action checks run before handlers; the error code grants no access. |
| Operation log | Existing mutation recorder remains responsible for success/failure; no new log channel is added. |
| Security log | Existing credential and service-account security logging remains unchanged. |
| Cache | A rejected stale write must not invalidate cached authoritative data. |
| Distributed lock | No new lock: database version predicates remain the concurrency boundary. |
| Optimistic lock | A zero-row `WHERE id=? AND version=?` update is `StaleVersion`; unrelated uniqueness/reference failures stay `Conflict`. |
| Audit | Existing transaction actor and audit triggers remain authoritative. |
| Presentation | User-facing text is generic; clients use the stable code, not message parsing. |
| Tests | Unit tests pin the shared code, handler envelope and representative zero-row mutation behavior; tagged integration tests remain a later CI gate. |
| Shared capability | Reuse the SDK's `StaleVersion` code through the central `apperror` adapter. |
| Data dictionary | Error responses are not dictionary data. |
