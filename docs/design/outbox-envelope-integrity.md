# Outbox envelope identity integrity

| Concern | Decision and rationale |
| --- | --- |
| Contract | The persisted `event_outbox.id` is the durable event identity. Before publishing, the dispatcher requires the protobuf envelope's `event_id` to equal that row ID. Malformed or mismatched envelopes take the existing bounded retry/dead-letter path; they are never published. This is an internal worker contract, not a new HTTP/gRPC API. |
| Authentication and authorization | Only the internal dispatcher reads outbox rows; producers use the existing authenticated, actor-scoped transactional outbox. No public route or permission is added. |
| Operation and security logs | No new log event is emitted by the dispatcher, avoiding recursion. Existing safe `decode_failed` error and bounded dispatch metrics are retained; raw protobuf data and IDs are not added to trace error text. |
| Cache and distributed lock | Neither is used. The database claim lease and JetStream event ID retain their existing ownership and duplicate-delivery roles. |
| Optimistic lock and audit | No business row is updated. Existing outbox claim/release updates and audit actor handling remain unchanged. |
| Presentation and dictionary | Not applicable to an internal queue worker; outbox events are not dictionary data. |
| Tests | Unit tests cover matching, mismatched, empty and malformed event IDs. CI integration tests continue to cover transactional outbox dispatch; local development runs only unit tests. |
| Shared capability | Reuses the existing platform event bus, outbox, error sanitization and retry/dead-letter path. No parallel queue transport is introduced. |

This check protects against corrupted or manually replayed outbox rows. A
producer already stores the same event ID in the row and protobuf payload, but
the dispatcher must verify that invariant again at its own trust boundary.
