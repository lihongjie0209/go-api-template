# Scheduled-job update/delete action targets

| Concern | Decision and rationale |
| --- | --- |
| Contract | `POST /api/v1/scheduled-jobs/update-target` and `/delete-target` accept `{id}`. Update target returns immutable code plus all editable fields and version; delete target returns identity, status and version only. Shared envelope/errors apply. |
| Authentication/authorization | JWT; targets bind to `scheduled-job:update` and `scheduled-job:delete` respectively, without requiring list/read. Platform scope; no row data policy. |
| Operation/security log | Target lookups emit neither. Existing update/delete transactions record operation logs; neither changes credentials. |
| Cache/lock | Read current database state without cache. No distributed lock; update/delete use expected version and database predicates, while runtime refresh occurs after commit. |
| Optimistic lock/audit | Zero affected rows becomes the global stale-version error, distinct from definition identity conflict. Existing transaction actor and audit triggers maintain fields. |
| Presentation | Update target includes valid JSON payload and no audit/actor data; delete target has only code/name/status/version. Time display is unchanged. |
| Tests | SQL/handler units cover target success, missing/invalid/deleted records and error mapping. Frontend API/page/real-capability units cover action-only access, stale target invalidation and post-commit sync. Existing Testcontainers lifecycle exercises update/delete when CI next runs; no local integration execution. |
| Shared capability/dictionary | Reuse known-action target, editor, authorized action button and error envelope. Job definitions are transactional operational data, not dictionary options. |
