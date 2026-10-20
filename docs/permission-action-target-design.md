# Permission action edit target

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/permissions/actions/target` accepts `{ "id": "uuid" }` and returns a narrow `{id,permission_key,name,resource,action,version,actions}` body in the shared envelope. Invalid UUID, missing/deleted node, and non-editable group/system node use global invalid/not-found/conflict errors. No pagination: at most 100 linked actions are allowed by the existing mutation contract. |
| Authentication | JWT only, with the route bound to `permission.definition:update`. |
| Authorization | Platform-scoped permission definition; the route does not grant read/list/tree access. The backend rechecks the target type and system flag. `actions/set` remains the authoritative write check. |
| Operation log | None for the target read. `permission.actions.set` already records the write. |
| Security log | None for the target read. The existing privilege-change write path records the security event. |
| Cache | None; the target and version must be fresh before optimistic mutation. |
| Distributed lock | None; the target is read-only, while the existing transaction/version predicate protects the write. |
| Optimistic lock | Target returns the current version; `actions/set` requires and checks it. |
| Audit | No write; the existing mutation uses actor-aware transaction/audit maintenance. |
| Presentation | Return the permission name and linked resource/action names, not opaque IDs alone. No timestamps in this narrow response. |
| Tests | Service and handler unit tests cover valid target and missing/group/system target. The route is bound to the shared update descriptor, while live authorization remains for the later integration pass. Frontend unit tests cover update-only access, invalid/stale targets, and versioned save. |
| Shared capability | Reuse the existing permission service, shared response/error envelope, route authorization descriptor, and frontend selection drawer. No new infrastructure abstraction. |
| Data dictionary | No provider: permission associations are sensitive authorization configuration, not a public bounded dictionary. |

The permission matrix gains one platform-scoped row:

| Operation | Auth | Resource/action | Object check | Data filter |
| --- | --- | --- | --- | --- |
| `/permissions/actions/target` | JWT | `permission.definition:update` | Existing editable permission node only | Not tenant-owned |
