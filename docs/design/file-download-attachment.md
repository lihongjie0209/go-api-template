# File download attachment contract

## `POST /api/v1/files/download`

| Concern | Decision and rationale |
| --- | --- |
| Contract | Accept `{id}` and return the existing short-lived signed URL, expiry, and file metadata. The URL is signed with `Content-Disposition: attachment` and the stored original filename, so cross-origin browser downloads do not depend on the HTML `download` attribute. |
| Authentication | JWT; the existing `file.object:download` Resource/Action descriptor remains required. |
| Authorization | The file service resolves the record under the authenticated tenant ID and rejects cross-tenant or deleted records before signing. |
| Operation log | Record the existing file-download access event; do not log or persist the signed URL. A successful event must be enqueued before returning the URL. |
| Security log | None; this is not a credential or privilege operation. |
| Cache | None; every request gets a fresh bounded signed URL. |
| Distributed lock | None; download does not mutate an object. |
| Optimistic lock | None; read-only operation. |
| Audit | No audited table write except the existing asynchronous operation-log path. |
| Presentation | File metadata includes original name, size, content type, timestamps, and actor display names. Frontend still verifies the requested ID, expiry, and URL scheme. |
| Tests | Unit tests cover S3 and OSS signature query parameters, unsafe filenames, cross-tenant isolation, and operation-log failure; integration tests remain in CI. |
| Shared capability | Extend the existing object-storage `Store.Presign` input so both providers and observability/timeout decorators share the attachment contract. |
| Dictionary provider | Not applicable: files are tenant-owned transactional objects, not bounded reference data. |
