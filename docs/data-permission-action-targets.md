# Data-permission action-only targets

The status command is a separate permission from policy reading. A user with
only `data-permission.{global,tenant}-policy:set-status` needs a narrow,
authoritative target before submitting the expected version.

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/data-permissions/{global,tenant}-policies/status/target` accepts `{ "id": "uuid" }` and returns only `id`, `code`, `name`, `scope`, `status`, and `version`. Bad IDs use the global invalid-request code; missing or wrong-scope records use not-found. No pagination or filters apply to a single target. |
| Authentication | JWT, matching the status command. |
| Authorization | Route binds the same resource and `set-status` action as the write. The lifecycle service applies the actor's tenant scope; the handler also rejects the wrong policy scope. The target does not grant `read` or `list`. |
| Operation log | None: this is a narrow read. The existing status command records the mutation. |
| Security log | None: no credentials or grants change. |
| Cache | None: a fresh version is needed immediately before a write. |
| Distributed lock | None: the status command relies on its database version predicate. |
| Optimistic lock | The target returns the current version; the existing command supplies it as `expected_policy_version`. A conflict requires a new target lookup. |
| Audit | Read-only target; status writes retain the existing transaction-local actor and audit trigger. |
| Presentation | The target includes `name` and `code` for confirmation, never an opaque ID alone; no timestamps are returned. |
| Tests | Handler projection and wrong-scope unit tests; frontend capability, stale lookup, and conflict tests. Repository tenant isolation remains in the later CI integration pass. |
| Shared capability | Reuse the existing Resource/Action route binding, response/error envelope, `GoReadByIDPanel`, and `useKnownActionTarget`. No new shared SDK component is needed. |

Policy records are mutable authorization configuration, not a bounded static
dictionary. They are not exposed as a data-dictionary provider.

## Publish target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/data-permissions/{global,tenant}-policies/publish/target` accepts `{ "id": "uuid", "version_number": 1 }`. It returns policy `id`, `code`, `name`, `scope`, `status`, `version` plus `version_number` and `version_status`; no document or audit identity is returned. Missing policy/version is not-found, a non-draft target is a state conflict. No list/filter/sort applies. |
| Authentication | JWT, matching the publish command. |
| Authorization | Route binds the same scoped resource and `publish` action as the command. Repository lookups are actor-tenant scoped, the handler checks global/tenant route scope, and the version must belong to that policy. `read`/`list` are not implied. |
| Operation log | None for target lookup; the existing publish transaction emits its operation record. |
| Security log | None: this changes an authorization policy but not a user's credential or grant. |
| Cache | None: policy and draft status must be checked against the database before confirmation. |
| Distributed lock | None: publication already locks the policy and version rows in one transaction. |
| Optimistic lock | The target supplies `expected_policy_version`; publishing rechecks it and draft state in the transaction. On conflict the UI clears the target and requires another lookup. |
| Audit | Read-only target; existing publish transaction injects actor and updates audit fields. |
| Presentation | Policy name/code and current enabled/disabled state identify the target; version number is explicit. |
| Tests | Unit tests cover narrow projection, wrong scope, wrong version owner, non-draft conflict, frontend target validation, grant-only entry, conflict and unverified publish receipt. Tenant isolation/persistence remain for later CI integration. |
| Shared capability | Reuse route binding, error envelope, `GoReadByIDPanel`, `useKnownActionTarget`, and authorized action button. No new SDK abstraction. |

Published policies are mutable security configuration and remain unsuitable for
the static data-dictionary provider contract.

## Create-version target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/data-permissions/{global,tenant}-policies/versions/create-target` accepts `{ "id": "uuid" }` and returns `id`, `code`, `name`, `scope`, `tenant_id`, `version`. It does not disclose any historical policy document. A create-version-only operator must author the complete next YAML document. Wrong-scope/missing ID is not-found. |
| Authentication | JWT, matching the append-version command. |
| Authorization | Route binds `create-version` on the same scoped resource. The service reads under the actor's tenant boundary and the handler checks route scope; the write rechecks code and scope against the locked current policy. No `read` or `list` privilege is implied. |
| Operation log | None for target lookup; the existing create-version command records the append. |
| Security log | None: no credential, session, membership, or grant changes. |
| Cache | None: a fresh policy lock version is required. |
| Distributed lock | None: the existing transaction locks the policy row and uses the expected version. |
| Optimistic lock | Return `version`; a conflict clears the editor target and requires another lookup. Never retry a rejected draft automatically. |
| Audit | Read-only target; the existing append transaction maintains actor/audit fields. |
| Presentation | Return policy name/code for confirmation; tenant ID is included only for a tenant policy so the YAML scope can be constructed accurately. No user IDs or timestamps are returned. |
| Tests | Projection, wrong scope, tenant ID shape, frontend target validation, action-only page entry, YAML scope validation, conflict and stale-principal tests. Persistence stays for later CI integration. |
| Shared capability | Reuse the Resource/Action route binding, `GoReadByIDPanel`, `useKnownActionTarget`, and `PolicyDocumentDrawer`. No new SDK component. |

Versioned policy documents are mutable security configuration, not a static
data-dictionary provider.

## Tenant simulation context

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/data-permissions/tenant-policies/simulate/context` accepts `{}` and returns only `{ "tenant_id": "..." }`. It is the current JWT tenant, not a client-selected tenant. A token without tenant context receives the global forbidden/scope error. |
| Authentication | JWT. |
| Authorization | The route binds `data-permission.tenant-policy:simulate`, matching the simulator itself. It does not require unrelated `tenant.context:read` and cannot reveal another tenant. |
| Operation log | None: this is a narrow read of the caller's own context. |
| Security log | None: no authentication or privilege state changes. |
| Cache | None: the active tenant can change with the principal token and must be read from the current request context. |
| Distributed lock | None: no mutable state. |
| Optimistic lock | Not applicable. |
| Audit | No write; the existing simulator remains read-only. |
| Presentation | Return only the tenant ID needed for a valid simulation template; no user or membership details. |
| Tests | Unit tests cover tenant principal, missing tenant and missing principal, frontend response validation and principal-switch cancellation. |
| Shared capability | Reuse principal context, resource/action route binding and response envelope; no new SDK abstraction. |

This context is not a dictionary: it is request-specific and authorization-sensitive.

The same narrow `{tenant_id}` contract is also exposed at
`POST /api/v1/data-permissions/tenant-policies/create/context`, but that route
binds `data-permission.tenant-policy:create`. A create-only operator can seed
the mandatory policy scope without receiving `tenant.context:read`; the create
command still checks its actor boundary and policy scope at write time. It is
read-only, uncached, unlogged, and does not require a lock or optimistic version.
Unit tests cover both route modes and frontend principal-switch cancellation.
