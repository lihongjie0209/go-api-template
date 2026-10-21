# OpenAPI request and response media types

Swagger 2.0 inherits top-level `consumes` and `produces` unless an operation
overrides them. The API uses `application/json` as the documented default for
POST requests and responses. The exact file-upload route overrides only
`consumes` with `multipart/form-data`; its response remains JSON. Operational
and no-body routes may inherit the default without requiring a request body.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Keep existing paths, request/response models, and error codes. Declare top-level JSON media types so clients can generate headers for routes that omit repetitive operation annotations. Preserve multipart upload as an operation override. |
| Authentication | No change to public, JWT, PSK, or internal route descriptors. |
| Authorization | Existing Resource/Action checks, tenant scope, and file ownership remain authoritative. |
| Operation log | No runtime behavior changes; mutation and file event decisions remain with their handlers. |
| Security log | No runtime behavior changes; authentication and sensitive operations retain their existing event policies. |
| Cache | No new cache; documentation is generated from code annotations. |
| Distributed lock | No new lock; request semantics and mutation invariants are unchanged. |
| Optimistic lock | Existing versioned mutation inputs and predicates are unchanged. |
| Audit | No persistence changes; audit triggers and actor injection are unchanged. |
| Presentation | Generated clients can distinguish JSON from multipart and retain the shared response envelope. |
| Tests | Parsed Swagger tests check effective media types for every POST, multipart upload override, and that every non-empty POST body has a resolved field-bearing schema. Eight intentionally empty `{}` bodies are explicitly reviewed. Unit tests and `make swagger-check` verify generated artifacts. |
| Shared capability | Use one top-level Swagger contract instead of repeating media annotations across handlers. No SDK change is needed. |
| Dictionary provider | This documentation change does not change which data is exposed through dictionary providers. |
