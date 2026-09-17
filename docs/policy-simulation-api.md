# Policy Simulation API

Policy simulation validates one unpersisted candidate document and compares the
current published snapshot with an isolated candidate snapshot. It never writes
policy tables, publishes a version, sends a Redis notification, or replaces the
active runtime snapshot.

## Interface design review

| Concern | Decision and rationale |
| --- | --- |
| Contract | Four POST + JSON routes cover global/tenant PBAC and data-permission policies. Requests contain one candidate policy plus bounded typed evaluation input. Responses contain `baseline`, `candidate`, and `changed`. The simulated Resource/Action must be declared by that candidate; mismatches are invalid requests rather than synthetic deny decisions. Responses use the shared envelope. |
| Authentication | JWT only. Global simulation uses the corresponding platform policy-management permission; tenant simulation uses the tenant policy-management permission. |
| Authorization | Canonical actions are `pbac.*-policy:simulate` and `data-permission.*-policy:simulate`. Tenant routes require the candidate policy, simulated subject, and simulated resource tenant to equal the authenticated tenant. |
| Operation log | Simulation is a read-only diagnostic. Initial delivery relies on protected request telemetry; durable diagnostic operation logging is required before exposing full multi-policy production traces. |
| Security log | None. Simulation cannot change authorization state. Rejected attempts remain visible in request/security telemetry. |
| Cache | None. Candidate documents and hypothetical attributes must not be retained. |
| Distributed lock | None; each call owns an isolated immutable engine. |
| Optimistic lock | Not applicable because no state changes. |
| Audit | No database write occurs. |
| Presentation | PBAC returns baseline and candidate stable decision effect/reason and matched policy codes. Data permission returns baseline and candidate current/transition booleans plus parameterized SQL previews and parameter counts; parameter values are never returned. `changed` includes hidden SQL argument changes without exposing their values. |
| Tests | Unit tests cover active-policy composition, same-identity replacement, PBAC deny results, proposed-state denial, parameter redaction, target validation, tenant isolation, and cancellation. HTTP route/Swagger/authorization coverage remains enforced by existing CI gates. |
| Shared capability | Reuses the canonical registries, strict policy validators, CEL operation engine, data predicate compiler, and in-memory evaluator. |
| Dictionary | Not applicable; hypothetical authorization diagnostics are sensitive and request-specific. |

## Routes

- `POST /api/v1/pbac/global-policies/simulate`
- `POST /api/v1/pbac/tenant-policies/simulate`
- `POST /api/v1/data-permissions/global-policies/simulate`
- `POST /api/v1/data-permissions/tenant-policies/simulate`

Simulation accepts a policy document directly so an invalid draft can be
diagnosed before persistence. The baseline is evaluated from one immutable copy
of the current runtime snapshot. The candidate snapshot replaces an active
policy with the same `scope + tenant_id + metadata.code`; otherwise the
candidate is added. All other published global and applicable tenant policies
remain present, so PBAC deny-overrides and data-permission Allow-union minus
Deny-union semantics match runtime behavior. Database version simulation can be
layered on top later by loading the immutable version and passing the same model
to this service.

Data-permission simulation requires an explicit operation resource such as
`{"type":"tenant.member","tenant_id":"..."}` and an action because one
document may cover several actions. For a tenant resource, the authenticated
tenant, candidate-policy tenant, subject tenant, and resource tenant must all
match. Resource and subject attribute names are validated against the
code-owned Schema; arbitrary SQL and client-supplied column names are never
accepted.

Both simulators propagate request cancellation and reject an evaluation target
outside the candidate policy's declared Resource/Action set. A deny response
therefore represents evaluation of the candidate, not a miss caused by testing
an unrelated operation.
