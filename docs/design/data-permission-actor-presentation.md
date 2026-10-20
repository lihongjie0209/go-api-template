# Data-permission policy actor presentation

This change enriches existing policy and version create/get/page/publish/status responses. It does not add a route or alter authorization decisions.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Keep the existing POST + JSON requests, pagination, filters, error codes, and envelope. Add `created_by_name` and `updated_by_name` to policy/version responses, plus `published_by_name` to versions; nullable publication fields remain JSON `null` before publication. |
| Authentication | Keep the existing JWT route descriptors; no new public, PSK, or wildcard route. |
| Authorization | Keep each route's declared global/tenant policy resource and action. Name resolution does not grant access or change tenant/data scope. |
| Operation log | Existing mutations retain their operation events; reads do not add events because they only return already-authorized policy metadata. |
| Security log | Existing publication/status events remain unchanged; presentation adds no privileged mutation. |
| Cache | No new cache. The existing actor resolver is the source for current display names; a failed lookup falls back to a stable actor ID and logs a warning. |
| Distributed lock | None for presentation; no cross-instance contention invariant is introduced. |
| Optimistic lock | Existing mutation version checks remain mandatory; presentation happens after persistence and cannot report a committed mutation as failed. |
| Audit | Existing trigger-maintained actor IDs and timestamps remain authoritative; display names are response-only. |
| Presentation | Resolve page actor IDs in one batch, use the shared Asia/Shanghai timestamp formatter, and show names rather than opaque IDs in version UI. |
| Tests | Go unit tests cover batching, timezone, JSON fields, and resolver failure; frontend unit tests cover list/detail display. Integration tests remain CI-only. |
| Shared capability | Reuse `presentation.ActorNames` and the existing identity-backed actor resolver; no service-local lookup implementation. |

These policy records are not dictionary providers: policy documents and version history are sensitive transactional data, not bounded selector data.
