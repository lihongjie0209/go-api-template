# Known-ID file deletion target

## `POST /api/v1/files/delete-target`

| Concern | Decision |
| --- | --- |
| Contract | Accept `{id}` and return only `{id,original_name,version}`; validate the ID and return shared not-found/forbidden errors. The response contains no object key, signed URL, or storage metadata. |
| Authentication | JWT. |
| Authorization | `file.object:delete`, independent of file `read/list`; the owning file service still applies its tenant/creator SQL scope and logical-delete predicate. |
| Operation log | None for this read; the confirmed delete command records success/failure. |
| Security log | None; file deletion is an operation event, not a credential/session/privilege event. |
| Cache | None; the expected version must be current before confirmation. |
| Distributed lock | None for the read; the existing delete command owns object-deletion coordination. |
| Optimistic lock | Return the current version; the subsequent delete sends `{id,version}` and preserves version-conflict handling. |
| Audit | No write. The existing delete path maintains audit fields and logical deletion. |
| Presentation | Show the original filename in the confirmation, not only the ID; timestamps and other metadata are intentionally omitted. |
| Tests | Valid scoped target, missing ID, cross-tenant/not-found, response redaction, mismatch/invalid version, cancellation, permission and conflict handling. |
| Shared capability | Reuse the shared Resource/Action interceptor, response envelope, file service, and existing confirmation button; no service-local auth path. |
| Dictionary provider | Not suitable: files are transactional and potentially sensitive. |
