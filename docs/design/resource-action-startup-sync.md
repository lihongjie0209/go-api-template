# Backend Resource/Action Startup Registry

Backend Resource/Action definitions are declared in Go and reconciled into the
database after migrations, before HTTP and gRPC listeners start. The code
registry is authoritative only for `source=code`. Manual rows are reserved for
future administration and are never changed by this sync.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Startup sync takes the complete validated backend registry, build time and commit. Resource keys and action keys are immutable; names, descriptions and scopes are replaced from code. Missing code-owned rows become disabled. Manual records have protected POST management APIs. |
| Authentication | An in-process startup command uses the explicit `system:resource-catalog` principal. No public HTTP or gRPC registration endpoint exists. |
| Authorization | Only `source=code` rows may be updated or disabled. A manual key colliding with a code declaration aborts startup. Existing endpoint descriptors and their fail-closed checks remain in force. |
| Operation log | No queue operation event is emitted during startup; database audit records the system actor and each changed row. Startup failure is logged and prevents readiness. |
| Security log | No user security event: startup sync does not grant a principal or role. Registry source conflicts and stale builds fail startup. |
| Cache | No response cache is introduced. The immutable code registry remains the request-path resolver. Manual records can be linked to permissions, but a manual-only target requires a separately registered endpoint/provider before it can be evaluated at runtime. |
| Distributed lock | The audited `authorization_catalog_syncs` row is locked `FOR UPDATE` in the same transaction. The build-time/commit fence rejects an older or ambiguous build. |
| Optimistic lock | Not used for a startup snapshot: row-level versions are audit output, and the transaction lock serializes complete reconciliations. |
| Audit | All three catalog tables include the mandatory audit columns and triggers. Repeated identical startup produces no record updates. |
| Presentation | Names and descriptions are copied from code. Stable UUIDv5 IDs use the immutable catalog namespace, never an environment value. |
| Tests | Unit tests cover first sync, idempotence, overwrite, retirement, manual-source conflict and stale builds. Testcontainers integration covers PostgreSQL/MySQL migrations and real reconciliation without another service. |
| Shared capability | Reuses the SDK stable-ID helper and the existing audit-aware `database.Transactor`; no duplicate transaction or UUID implementation. |

The catalog is a security configuration registry, not a data-dictionary
provider. It is small and administered through deployment ownership; publishing
it through the public dictionary query contract would blur the authorization
boundary.

The operation authorizer uses permission-to-Resource/Action links and explicit
platform or tenant grants. Published operation policy documents are not used.
See [authorization-resource-grants.md](authorization-resource-grants.md) for
management endpoints, migration and bootstrap rules.
