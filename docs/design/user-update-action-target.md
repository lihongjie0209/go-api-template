# User update-only target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/users/update-target` accepts `{id}` and returns `{id,username,display_name,email,phone,status,version}` in the shared response envelope. Existing `/users/update` performs the versioned write. |
| Authentication | JWT; no anonymous or PSK path. |
| Authorization | Both target and write bind canonical `identity.user:update`. A principal must exist; the exact user ID is validated as a canonical UUID. Deleted rows are excluded. The target does not grant list/read capability. |
| Operation log | No log for the target read; existing update logs `identity.user.update`. |
| Security log | No event for target read; existing user update emits the identity-user-changed security event and revokes sessions when status becomes non-active. |
| Cache | Target bypasses the user cache to fetch the latest version. Existing update owns cache invalidation. |
| Distributed lock | None; the existing versioned transaction and constraints define the concurrency boundary. |
| Optimistic lock | Existing update requires positive `version`; a stale write is returned as version conflict and is never retried implicitly. |
| Audit | Existing update transactor injects the actor; database triggers maintain audit fields. |
| Presentation | Username is read-only identity. Contact fields are editable and therefore returned without masking only to callers holding update permission; UI masks them outside the edit form. No credential or token fields are returned. |
| Tests | Unit tests cover exact query, canonical ID and actor, not-found/error, handler response, frontend target validation and editor lifecycle. Testcontainers lifecycle covers freshness and deletion later in CI. |
| Shared capability | Reuses resource/action interception, `useKnownActionTarget`, `GoReadByIDPanel`, and `GoResourceEditor`; no new infrastructure copy. |
| Dictionary provider | User identity/contact data is sensitive transactional data and must not be a dictionary. |
