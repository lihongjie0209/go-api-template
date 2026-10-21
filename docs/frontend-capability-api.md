# Frontend Capability Evaluation

## Scope

The frontend consumes authorization decisions; it never downloads or evaluates
operation-authorization or data-permission documents. These decisions are presentation
hints only. Every business endpoint repeats its normal operation and data
authorization before reading or mutating authoritative state.

Two POST + JSON operations are supported:

- `POST /api/v1/authorization/capabilities/evaluate` evaluates up to 100
  page-level `resource + action` capabilities for the current principal.
- `POST /api/v1/authorization/rows/evaluate` evaluates up to 20 actions against
  up to 200 current rows of one tenant resource.

## Interface design review

| Concern | Decision and rationale |
| --- | --- |
| Contract | Both operations use the shared response envelope. Page requests carry a unique frontend correlation `key`, canonical resource, and action. Row requests carry only resource, actions, and IDs; duplicate, blank, or unknown values fail validation before the grant database is read. |
| Authentication | JWT only. The caller can evaluate only the principal and tenant already established by the verified token. |
| Authorization | Both routes bind `authorization.capability:evaluate` at principal scope. Authenticated subjects may evaluate their own capabilities. Requested capabilities are evaluated independently against registered target operations. |
| Operation log | None. These are high-frequency, read-only presentation queries with no sensitive data export. |
| Security log | None. Authentication and authorization failures remain visible through normal request/security telemetry. |
| Cache | No server response cache. Each capability request reads the current principal's effective operation grants from the authoritative database and pins that immutable set. It also pins the local published data-policy snapshot and hashes both snapshots into the opaque `revision`; row decisions use the same pinned data-policy snapshot even during refresh. Responses expire 30 seconds after evaluation; frontend caches must additionally be scoped to session and tenant. Business endpoints always recheck authoritative grants. |
| Distributed lock | None; evaluation is read-only over immutable policy snapshots. |
| Optimistic lock | Not applicable; no state is changed. |
| Audit | No table is written. Principal, tenant, endpoint context, subject roles, and department projections are all server-derived. |
| Presentation | Results expose only boolean decisions keyed by the caller correlation key or resource ID. Policy IDs, expressions, internal attributes, and deny reasons are not returned. |
| Tests | Unit tests cover target-operation selection, allow/deny/unavailable, bounded validation before dependency access, one tenant-scoped batch row query, provider ID and tenant-ID validation, missing-row concealment, current-row policy evaluation, grant-order-independent revisions, principal binding, and database failure. Snapshot regressions replace the published data policies between batch actions and verify that pinned row decisions and the data revision remain paired. The existing PostgreSQL/MySQL grant lifecycle integration test verifies that create/delete changes and restores the revision; execution remains a CI responsibility. |
| Shared capability | Reuses the Resource/Action registry, grant authorizer and data-permission evaluator. A missing revision service fails construction at startup; test compositions supply an explicit grant-snapshot fake instead of a nil revision dependency. The immutable endpoint registry indexes canonical Resource/Action targets at startup and returns defensive copies in stable order, avoiding a full route scan for each menu node. Bounded row evaluation is shared infrastructure rather than handler-local logic. |
| Dictionary | Not exposed as a dictionary: capability results are principal-, tenant-, policy-, and time-dependent security decisions. |

## Page capability semantics

One request item is:

```json
{"key":"member.update","resource":"tenant.member","action":"update"}
```

The backend resolves all registered JWT HTTP operations with that canonical
resource/action pair and evaluates the real target endpoint context. The result
is allowed when at least one target operation is allowed. The client cannot
invent trusted principal or resource attributes.

## Row capability semantics

One row request is:

```json
{
  "resource": "tenant.member",
  "actions": ["update", "remove"],
  "resource_ids": ["member-1", "member-2"]
}
```

For each action:

```text
row allowed = operation grant allowed AND current-row data permission allowed
```

The owning module's registered `RowCapabilityProvider` loads all requested rows
in one bounded, parameterized query
containing the authenticated `tenant_id` and `deleted_at IS NULL`. It resolves
subject projections once and evaluates the immutable data-policy snapshot in
memory. The shared evaluator rejects provider output outside the requested ID
set, whose trusted `id` differs from its map key, or whose trusted `tenant_id`
differs from the authenticated tenant. Every provider must return both fields.
Missing, deleted, and
cross-tenant IDs are indistinguishable and return `false` for every requested
action.

Only `tenant.member`, `tenant.department`, and `tenant.role` have row providers
in the first release. Adding another resource requires a module-owned provider,
registered data-permission schema, tenant-isolation tests, and documented
trusted attributes. Clients cannot submit resource attributes.

Provider registration is validated during dependency graph construction. The
application refuses to start when a provider names an unknown resource, a
non-tenant resource, or a resource without a registered data-permission Schema;
the same constructor tests run in CI.

`proposed_condition` is intentionally not evaluated because this API has no
validated target object. A normal edit button may be shown when the current row
is allowed; the mutation endpoint still evaluates the proposed transition and
may reject a disallowed target value.

## Frontend use

Page-level capability controls whether an edit column or general action entry
is shown. Each row button additionally consumes its row decision:

```text
show row edit = page member.update AND row member.update
```

The frontend must treat a failed or unavailable capability response as denied.
It must never use an earlier capability result as proof of authorization when
submitting a business operation.

The frontend may retain a result only until `expires_at` and only within the
same authenticated session and tenant. A changed `revision` invalidates all
capability results immediately. The revision is an opaque digest of the current
principal/session's effective platform/tenant operation grants and the published
data-permission snapshot's content. Grant queries fail closed, so an unavailable database
cannot emit a stale revision. Clients compare the digest for equality and must
not parse it. Changes that do not alter the effective grant set need not change
the digest; direct business requests remain authoritative.
