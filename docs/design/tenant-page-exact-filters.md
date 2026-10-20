# Tenant page exact filters

`POST /api/v1/tenants/page` and `POST /api/v1/platform/tenants/page` share this contract extension. The normal endpoint remains restricted to the principal's tenant; the platform endpoint requires platform authorization.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Add optional `codes` and `owner_user_ids` exact `IN` filters, each limited to 200 entries. Codes are trimmed, lowercased and validated against the canonical tenant-code format; owner IDs are trimmed, non-empty and at most 128 bytes. Empty arrays mean no filter. Both combine with existing keyword, ID, status and half-open creation-time filters by `AND`. Page size, ordering and global error codes remain unchanged. This lets operators search by the identifier or responsible user without a lossy keyword match. |
| Authentication and authorization | Existing JWT route descriptors and platform/tenant scopes are unchanged. A tenant caller cannot expand results by supplying another tenant's code or owner ID. |
| Operation and security logs | No log: this is a query, not a sensitive export or mutation. Existing request telemetry remains. |
| Cache and distributed lock | Neither: this is an authoritative bounded database query and creates no contention invariant. |
| Optimistic lock and audit | Not applicable to a read. Existing audited records and version fields are returned unchanged. |
| Presentation | Existing Asia/Shanghai timestamps and owner `{id,name}` reference remain; the new filters do not affect display fields. |
| Tests | Unit coverage checks bounds and canonicalization, plus matching count/list SQL arguments under the tenant predicate. Cross-tenant persistence and database adapter behavior remain CI integration concerns. |
| Shared capability | Reuse shared pagination, response and authorization. No new cross-service component is needed. |
| Dictionary provider | No: tenants and their owners are transactional, potentially sensitive resources, not bounded static dictionary entries. |

`code` already has a unique constraint; `owner_user_id` is queried through the existing tenant table. If operational plans show expensive owner filtering at scale, add an index in an independently reviewed migration.

## Repository scope hardening

| Concern | Decision and rationale |
| --- | --- |
| Contract | Keep both POST page requests, filters, stable sort, response envelope and global errors unchanged. `Repository.Page` rejects an empty tenant ID with the existing forbidden error; `Repository.AdminPage` is the only explicitly named unscoped repository entrypoint. |
| Authentication | Tenant page remains JWT; platform page remains JWT with the platform route descriptor. No PSK or anonymous access is added. |
| Authorization | The transport checks `tenant.profile:list` for the current-tenant page or platform `tenant:list` for the admin page before the service call. Service-level tenant context remains mandatory for normal page; the repository independently fails closed if it is missing. |
| Operation log | None: bounded query only. No mutation or export occurs. |
| Security log | None: this is not an authentication, privilege-changing, or suspicious action. |
| Cache | None: page/count use authoritative database state; no invalidation owner or TTL is needed. |
| Distributed lock | None: read-only pagination has no cross-instance contention invariant. |
| Optimistic lock | Not applicable to a query; returned record versions remain unchanged. |
| Audit | No write or actor injection; existing audit fields are read without modification. |
| Presentation | Existing related owner display name and RFC 3339 time formatting remain unchanged. |
| Tests | Unit tests prove empty tenant ID causes no query, scoped count/items share the tenant predicate, and the explicit admin method stays unscoped. Existing Testcontainers tenant lifecycle covers database compatibility in CI. |
| Shared capability | Reuse the existing repository, pagination, authorization and error mapping; no new SDK component is needed. |
| Dictionary provider | None: tenant records are transactional and sensitive, not bounded reference data. |
