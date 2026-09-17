# Frontend Capability Evaluation

## Scope

The frontend consumes authorization decisions; it never downloads or evaluates
PBAC or data-permission policy documents. These decisions are presentation
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
| Contract | Both operations use the shared response envelope. Page requests carry a unique frontend correlation `key`, canonical resource, and action. Row requests carry only resource, actions, and IDs; duplicate or blank values fail validation. |
| Authentication | JWT only. The caller can evaluate only the principal and tenant already established by the verified token. |
| Authorization | Both routes bind `authorization.capability:evaluate` at principal scope. A global PBAC policy should allow authenticated subjects that may use the UI. Requested capabilities are evaluated independently against their registered target operations. |
| Operation log | None. These are high-frequency, read-only presentation queries with no sensitive data export. |
| Security log | None. Authentication and authorization failures remain visible through normal request/security telemetry. |
| Cache | No server response cache initially. Published policy snapshots are already in memory. A later frontend cache must be scoped to session and tenant and use a short TTL. |
| Distributed lock | None; evaluation is read-only over immutable policy snapshots. |
| Optimistic lock | Not applicable; no state is changed. |
| Audit | No table is written. Principal, tenant, endpoint context, subject roles, and department projections are all server-derived. |
| Presentation | Results expose only boolean decisions keyed by the caller correlation key or resource ID. Policy IDs, expressions, internal attributes, and deny reasons are not returned. |
| Tests | Unit tests cover target-operation selection, allow/deny/unavailable, bounded validation, one tenant-scoped batch row query, missing-row concealment, and current-row policy evaluation. Integration execution remains a CI responsibility. |
| Shared capability | Reuses the PBAC registry/authorizer and data-permission evaluator. Endpoint lookup and bounded row evaluation are shared infrastructure rather than handler-local logic. |
| Dictionary | Not exposed as a dictionary: capability results are principal-, tenant-, policy-, and time-dependent security decisions. |

## Page capability semantics

One request item is:

```json
{"key":"member.update","resource":"tenant.member","action":"update"}
```

The backend resolves all registered JWT HTTP operations with that canonical
resource/action pair and evaluates the real target endpoint context. The result
is allowed when at least one target operation is allowed. This preserves PBAC
`when` semantics such as target `request.operation`, authentication scheme, and
working hours without letting the client invent trusted operation attributes.

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
row allowed = operation PBAC allowed AND current-row data permission allowed
```

The backend loads all requested rows in one allowlisted, parameterized query
containing the authenticated `tenant_id` and `deleted_at IS NULL`. It resolves
subject projections once and evaluates the immutable data-policy snapshot in
memory. Missing, deleted, and cross-tenant IDs are indistinguishable and return
`false` for every requested action.

Only `tenant.member`, `tenant.department`, and `tenant.role` have row providers
in the first release. Adding another resource requires an allowlisted loader,
registered data-permission schema, tenant-isolation tests, and documented
trusted attributes. Clients cannot submit resource attributes.

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
