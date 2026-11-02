# Event bus startup deadline

The shared event-bus SDK connects to NATS and then provisions the JetStream
stream. Its connection timeout does not bound stream provisioning because that
step uses the Context supplied by the caller. The Fx constructor therefore
supplies a bounded Context. `event_bus.startup_timeout` defaults to 15 seconds
and must be between `connect_timeout` and one minute. The connection timeout
cannot exceed one minute, and at most eight NATS URLs may be configured.
A failure aborts service startup. The NATS client owns initial connection
attempts, so with multiple unreachable URLs total construction time may exceed
`startup_timeout`; the configured per-endpoint connection timeout and URL cap
bound those attempts separately.
Fx receives a fixed dependency error for connection/provisioning failures,
because SDK errors may include credential-bearing NATS URLs. A known timeout
or cancellation remains distinguishable without exporting its raw wrapper.
An enabled constructor returning no client, or returning only after its
deadline, is rejected before any lifecycle stop hook is registered.

| Concern | Decision and rationale |
| --- | --- |
| Contract | Event-bus construction succeeds only after connection and stream provisioning; a timeout returns the constructor error and stops Fx startup. No business API shape changes. |
| Authentication and authorization | NATS credentials remain in the existing connection configuration; business Resource/Action checks are unaffected. |
| Operation and security logs | The outbox and durable consumers are not started after a failed event-bus constructor. A deployed environment already requires the event bus and both log channels. |
| Cache and distributed lock | No cache or lock is involved in construction. Redis remains an independent mandatory dependency. |
| Optimistic lock and audit | Startup makes no service-owned row mutation. Stream provisioning belongs to the shared SDK and is bounded by this Context. |
| Presentation and observability | The startup error is visible to the operator through Fx; request clients cannot receive a success from a service that failed to start. No credential-bearing dependency error is copied into an HTTP response. |
| Tests | Configuration unit tests reject missing, inverted, and excessive deadlines. An injected constructor waits for its Context and proves startup terminates with deadline exceeded without registering a stop hook. Local config, event-bus and app unit tests pass; Testcontainers remains CI-only. |
| Shared capability | The template continues to call the shared event-bus SDK rather than opening a second NATS connection. The separate consumer subscription-ready signal remains an SDK contract gap for future queue readiness reporting. |
