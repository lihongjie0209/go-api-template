# Backend Resource/Action Startup Registry

Backend Resource/Action definitions are declared in Go and reconciled into the
database after migrations, before HTTP and gRPC listeners start. The code
registry is authoritative only for `source=code`. Manual rows are reserved for
future administration and are never changed by this sync.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Startup sync takes the complete validated backend registry, build time and commit. Resource keys and action keys are immutable; names, descriptions and scopes are replaced from code. Missing code-owned rows become disabled. There is no request API in this phase. |
| Authentication | An in-process startup command uses the explicit `system:resource-catalog` principal. No public HTTP or gRPC registration endpoint exists. |
| Authorization | Only `source=code` rows may be updated or disabled. A manual key colliding with a code declaration aborts startup. Existing endpoint descriptors and their fail-closed checks remain in force. |
| Operation log | No queue operation event is emitted during startup; database audit records the system actor and each changed row. Startup failure is logged and prevents readiness. |
| Security log | No user security event: startup sync does not grant a principal or role. Registry source conflicts and stale builds fail startup. |
| Cache | No response cache is introduced. The current immutable code registry remains the request-path resolver. A later manual-registration phase must add a revisioned runtime catalog before those entries can be authorized. |
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

The current authorization engine still uses the in-memory Resource/Action
registry and published PBAC policies. Persisting the backend catalog is the
first step toward a simpler permission-to-Resource/Action mapping; it does not
silently change effective grants. A future phase will remove PBAC only after
the replacement authorizer and permission mappings are in place.
