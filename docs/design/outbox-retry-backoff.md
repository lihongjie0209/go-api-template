# Transactional outbox retry backoff

| Concern | Decision and rationale |
| --- | --- |
| Contract | A failed outbox publish remains in the database and is retried after a bounded delay, even after `consumer_max_deliver` attempts, so a broker outage cannot discard a valid business event. `dispatch_retry_delay` is the first retry's minimum; each subsequent failed claim doubles the delay until `dispatch_retry_max_delay`. A positive random jitter of up to 20% spreads multiple dispatchers, without exceeding the cap. Defaults are 2 seconds and 1 minute; the cap must be at least the base and at most 1 hour. `consumer_max_deliver` applies only to deterministically invalid stored rows (subject, event identity, or envelope). No HTTP/gRPC contract changes. |
| Authentication and authorization | Only the internal dispatcher advances a claimed row. The existing system actor and owner-fenced SQL update remain unchanged. No public route or permission is added. |
| Operation and security logs | No new audit event is emitted by retry scheduling. The existing bounded `publish_failed` or `decode_failed` reason and low-cardinality dispatch metric remain. Event payloads and broker errors do not enter the persisted failure reason. |
| Cache and distributed lock | Neither is used. The database lease remains the coordination authority; `available_at` is the durable retry schedule. |
| Optimistic lock and audit | The existing claim owner predicate, audit trigger, version increment and dead-letter transition are retained. Only deterministically invalid rows receive `dead_at` after exhausting the configured attempts; broker failures always retain a durable `available_at` retry. |
| Presentation and dictionary | Not applicable to an internal queue worker. Dead rows remain visible through the existing payload-free `outboxctl` listing and versioned replay. |
| Tests | Unit tests pin the first, second and third retry ranges, jitter bound, saturation for a large attempt count, configurable maximum, SQL `available_at` wiring, broker-outage recovery past the delivery limit, invalid-row dead-lettering and safe persisted reason. Testcontainers integration stays in GitHub CI; local development runs unit tests only. |
| Shared capability | Reuses the existing outbox, NATS JetStream publisher, actor-aware transaction and dead-letter repair. No second message transport is introduced. |

The broker's finite duplicate window is not a correctness guarantee: a retry
may occur later than that window. Consumers must continue to deduplicate by
the canonical event ID, including after manual replay.
