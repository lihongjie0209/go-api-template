# Policy Simulation API

Policy simulation validates and evaluates one unpersisted candidate document in
an isolated in-memory engine. It never writes policy tables, publishes a
version, sends a Redis notification, or replaces the active runtime snapshot.

## Interface design review

| Concern | Decision and rationale |
| --- | --- |
| Contract | Four POST + JSON routes cover global/tenant PBAC and data-permission policies. Requests contain one candidate policy plus bounded typed evaluation input. Responses use the shared envelope. |
| Authentication | JWT only. Global simulation uses the corresponding platform policy-management permission; tenant simulation uses the tenant policy-management permission. |
| Authorization | Canonical actions are `pbac.*-policy:simulate` and `data-permission.*-policy:simulate`. Tenant routes require the candidate policy, simulated subject, and simulated resource tenant to equal the authenticated tenant. |
| Operation log | Simulation is a read-only diagnostic. Initial delivery relies on protected request telemetry; durable diagnostic operation logging is required before exposing full multi-policy production traces. |
| Security log | None. Simulation cannot change authorization state. Rejected attempts remain visible in request/security telemetry. |
| Cache | None. Candidate documents and hypothetical attributes must not be retained. |
| Distributed lock | None; each call owns an isolated immutable engine. |
| Optimistic lock | Not applicable because no state changes. |
| Audit | No database write occurs. |
| Presentation | PBAC returns stable decision effect/reason and matched policy codes. Data permission returns current/transition booleans plus a parameterized SQL preview and parameter count; parameter values are never returned. |
| Tests | Unit tests cover PBAC deny results, proposed-state denial, parameter redaction, validation failures, and cancellation. HTTP route/Swagger/authorization coverage remains enforced by existing CI gates. |
| Shared capability | Reuses the canonical registries, strict policy validators, CEL operation engine, data predicate compiler, and in-memory evaluator. |
| Dictionary | Not applicable; hypothetical authorization diagnostics are sensitive and request-specific. |

## Routes

- `POST /api/v1/pbac/global-policies/simulate`
- `POST /api/v1/pbac/tenant-policies/simulate`
- `POST /api/v1/data-permissions/global-policies/simulate`
- `POST /api/v1/data-permissions/tenant-policies/simulate`

Simulation accepts a policy document directly so an invalid draft can be
diagnosed before persistence. Database version simulation can be layered on top
later by loading the immutable version and passing the same model to this
service.

Data-permission simulation requires an explicit action because one document may
cover several actions. Resource and subject attribute names are validated
against the code-owned Schema; arbitrary SQL and client-supplied column names
are never accepted.
