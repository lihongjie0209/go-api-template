# Current navigation tenant-status boundary

The current-navigation endpoint is a tenant-scoped read. An active membership
and application grant do not authorize navigation for a disabled or deleted
tenant. The revision query must establish an active tenant before reading a
cached or uncached navigation source; this keeps the cache from bypassing the
tenant lifecycle check.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Keep `POST /api/v1/me/navigations`, its request/response models and global errors unchanged. A disabled tenant returns an empty tree, as other unavailable grants do. |
| Authentication | JWT only, through the existing route descriptor. |
| Authorization | Keep `navigation.current:read` and tenant/member/grant SQL checks; add `tenants.status='active' AND tenants.deleted_at IS NULL` to the revision query. |
| Operation log | None: this is a routine read. |
| Security log | None: this is not an authentication or privilege mutation. |
| Cache | Keep the revisioned shared source cache; never inspect it unless the live SQL authorization query succeeds. |
| Distributed lock | None: read-only endpoint with no cross-instance invariant. |
| Optimistic lock | Not applicable to a read. |
| Audit | No write; existing audited tables remain unchanged. |
| Presentation | Existing ordered tree shape and labels remain unchanged. |
| Tests | Unit test asserts the active-tenant SQL boundary; integration test verifies disabled tenants receive no navigation. |
| Shared capability | Reuses the existing navigation tree/cache and authorization descriptor; no new SDK abstraction. |
| Dictionary provider | Navigation is application/user-contextual and not a bounded static dictionary. |
