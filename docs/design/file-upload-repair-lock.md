# File upload and repair lease

The upload intent is durable before the object write. A stale-intent worker
must not delete that object while the original upload is still writing it or
committing its file row. Both paths now acquire the same renewable
`file:upload:<file-id>` lease. The normal upload holds it from intent creation
through the object write and final metadata, intent, and operation-event
transaction. Failure repair starts only after the upload releases the lease.
Once the final transaction commits, a lease-release error is observed by the
shared committed-lock helper without turning the successful upload response
into a reported failure.
The repair worker checks cancellation between the upload-intent and deletion
scans so shutdown does not start another database query after its lifecycle
Context ends.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Existing multipart upload and file response remain unchanged. A failed lease acquisition returns the global dependency-unavailable envelope before intent or object write. A lease lost before final commit cancels the metadata transaction, leaves the durable intent for repair, and returns the same dependency code. The raw Redis error is not returned. |
| Authentication | Existing JWT/PSK route descriptor remains authoritative. The repair worker uses its system principal. |
| Authorization | The uploaded file is bound to the authenticated tenant and actor through the existing file scope. Repair may address only the generated upload ID. |
| Operation log | Successful metadata and operation event commit together. A failed upload records its existing safe operation failure. |
| Security log | None: upload and repair do not change credentials, grants, sessions, or memberships. |
| Cache | No upload response cache: file metadata changes during upload and repair. |
| Distributed lock | A renewable Redis lock is scoped to the generated file ID and shared with stale-intent repair. The worker retries on contention; neither path deletes an object while the other holds the lease. Lease loss cancels the upload's database and object-storage Context. |
| Optimistic lock | The final intent transition requires pending state, the file row is inserted once, and the object key is unique per upload. An upload has no prior file version to compare. |
| Audit | Database audit triggers maintain actor, time, version and logical-delete fields on the file and intent rows. |
| Presentation | File response names and +08 times retain the existing presentation path. |
| Tests | A service-local unit test makes the repair worker attempt the same lock during object `Put`, checks that it cannot enter, and checks that the lock remains held through `RecordTx` even when release reports an error. Acquisition and renewal failures are tested for safe dependency classification; renewal loss cancels the callback. The HTTP boundary maps the sentinel to 503 with the global dependency code. Existing failure-repair unit tests still pass. The PostgreSQL/MySQL Testcontainers suite combines a real database, the shared Redis locker and an in-process Redis server to prove that a due repair cannot delete an active upload object; CI execution is pending. |
| Shared capability and dictionary | Reuses SDK `cache.WithCommittedLock` and `cache.WithLock`. Upload intents are sensitive transactional state and are not dictionary data. |
