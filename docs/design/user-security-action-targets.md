# User security action targets

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/users/security-target/reset-password` and `/force-logout` accept `{ "id": "user UUID" }` and return only `{id,username,display_name,status}` in the shared envelope. Malformed/closed/deleted IDs use shared invalid/not-found errors. No credential, contact, or session fields are returned. |
| Authentication | JWT only. These are not public or PSK routes. |
| Authorization | The reset target binds `identity.user:reset-password`; the logout target binds `identity.user:force-logout`. Neither implies `identity.user:read/list`. The existing mutation routes repeat their own action authorization. |
| Operation log | No operation event for target reads. Mutations retain their existing operation/security recording. |
| Security log | No event for normal target reads. Password reset and forced logout retain security events; suspicious denied requests are handled by the shared authorization layer. |
| Cache | None. Target confirmation needs current status and is read directly from the owning repository, bypassing the general user cache. |
| Distributed lock | None for reads. Existing mutation transaction/session rules remain authoritative. |
| Optimistic lock | Read only; existing credential/session mutations use their own transactional version controls, not the user-row version. No automatic mutation retry. |
| Audit | Read only. Existing mutations retain actor/audit trigger behavior. |
| Presentation | Username and display name accompany the ID. Closed users are not actionable; no email/phone is exposed. |
| Tests | Identity and handler unit tests cover UUID validation, missing targets, narrow response, and both action routes. Frontend API/page tests cover action-specific lookup, confirmation, and input invalidation; the shared known-target tests cover denial, stale lookup, and principal changes. The existing Testcontainers identity lifecycle now checks active/deleted targets and rejects forced logout of a deleted user; it will run in CI after the next substantial feature. |
| Shared capability | Reuse identity repository conventions, shared response/error mapping, `useKnownActionTarget`, capability provider, reset drawer, and authorized action button. |
| Dictionary provider | No; user identities and security operations are sensitive transactional data. |

| Route | Principal | Resource/action | Scope | Data filtering |
| --- | --- | --- | --- | --- |
| `/users/security-target/reset-password` | JWT | `identity.user:reset-password` | Platform user object | Global identity user, non-deleted and non-closed |
| `/users/security-target/force-logout` | JWT | `identity.user:force-logout` | Platform user object | Global identity user, non-deleted and non-closed |
