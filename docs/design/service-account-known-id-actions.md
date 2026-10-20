# Service-account known-ID actions

## `POST /api/v1/service-accounts/secret/rotate-target` and `/delete-target`

| Concern | Decision |
| --- | --- |
| Contract | Accept `{id}` and return only `{id,client_id,name,status,version}` for confirmation. Empty/oversized IDs fail validation; deleted accounts are not found. |
| Authentication | JWT platform operator. |
| Authorization | Rotate target requires `identity.service-account:rotate-secret`; delete target requires `identity.service-account:delete`. Neither requires unrelated `read/list`. The existing mutation repeats authorization and version checks. |
| Operation log | No event for target reads; the existing rotation/deletion commands record operation results. |
| Security log | No event for target reads; the existing commands emit service-account security events. |
| Cache | None, because status and optimistic version must be fresh. |
| Distributed lock | None for reads. Existing mutation transaction and version predicate are authoritative. |
| Optimistic lock | The target supplies the current version; the subsequent command requires it and never retries an outdated version. |
| Audit | Read-only target. Existing commands maintain audit and logical deletion. |
| Presentation | Show client ID and name before confirmation; never include secret, hash, or one-time reveal in the target response. |
| Tests | Narrow response, invalid ID, deleted/not-found, permission-only entry, stale lookup/principal switch, version conflict, and one-time secret handling. |
| Shared capability | Reuse Resource/Action interceptor, shared envelope, known-ID panel, authorized action button, and secret-reveal modal. |
| Dictionary provider | Not suitable: service accounts are sensitive transactional identities. |

The two routes intentionally share an HTTP implementation but have separate
authorization descriptors. Operators with only one action cannot use the
other action's target endpoint.
