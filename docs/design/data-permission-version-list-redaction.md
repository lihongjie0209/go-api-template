# Data-permission version list redaction

The `versions/page` route requires `list`, while `versions/get` requires `read`.
A list response must therefore not include a policy document. The frontend
loads the selected version through `get` before viewing, copying, or simulating
it. A user with `list` but not `read` can see version metadata but not content.

| Concern | Decision and rationale |
| --- | --- |
| Contract | `versions/page` returns a bounded page of version metadata without `document`; `versions/get` returns the complete version. Existing page and status filters remain unchanged. |
| Authentication | Both routes require JWT. |
| Authorization | `list` permits metadata only; `read` is required for the document. The owning handler still checks global/tenant scope. |
| Operation log | No new log: both operations are reads of policy metadata/content under existing access controls. |
| Security log | No new security event: no credential or grant is changed. |
| Cache | None: policy content and publication state must reflect the authoritative database. |
| Distributed lock | None: reads have no cross-instance invariant. |
| Optimistic lock | Not applicable to reads; version creation and publication retain expected parent versions. |
| Audit | Existing database audit fields are returned as metadata; no write occurs. |
| Presentation | Timestamps remain RFC 3339; creator and publisher display names remain available. |
| Tests | Unit tests verify document omission and metadata preservation; frontend tests verify detail fetch before simulation and read-denied behavior. |
| Shared capability | Reuses the registered Resource/Action authorization and common response envelope. No new SDK component is needed. |
| Dictionary provider | Not suitable: policy documents are sensitive administrative data, not bounded dictionary values. |
