# gRPC idempotency route approval

| Concern | Decision and rationale |
| --- | --- |
| Contract | `idempotency.grpc_methods` selects only already registered unary methods. A pattern that matches no method, or also matches an unapproved method, fails startup. No RPC is approved by default because current identity methods are reads or authentication operations. |
| Authentication and authorization | Existing authentication and Resource/Action interceptors run before the shared SDK idempotency interceptor. Approval does not grant permission; each request and replay must pass current operation authorization. Token-issuing methods are not eligible for response replay. |
| Operation and security logs | No new log event is emitted. Existing business security-event requirements remain with the owning RPC. Approval cannot be used to bypass durable audit logging. |
| Cache and distributed lock | The existing SDK Redis idempotency state machine remains the only coordination mechanism. No second cache or lock implementation is introduced. |
| Optimistic lock and audit | Future approved mutations must still use transactional constraints and expected versions; Redis does not make a database write atomic. Current methods are not newly approved. |
| Presentation and dictionary | No response model changes. Existing identity reads are unsuitable for idempotency response storage, not dictionary providers. |
| Tests | Unit cases cover approved unary, unknown, unapproved, wildcard, infrastructure and streaming methods, plus the registered sensitive identity method. Service-local integration CI remains the transport gate. |
| Shared capability | Reuses the SDK interceptor and code-owned approval principle already used for HTTP; only runtime gRPC method-table validation is transport-specific. |
