package observability

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
)

// CaptureTraceContext serializes only the W3C trace context needed to link
// durable work to its caller. Baggage and other request metadata are excluded.
func CaptureTraceContext(ctx context.Context) (string, string) {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	parent, state := carrier.Get("traceparent"), carrier.Get("tracestate")
	if len(parent) > 55 || len(state) > 512 {
		return "", ""
	}
	return parent, state
}

// RestoreTraceContext accepts only bounded, valid W3C trace context. An
// invalid persisted parent leaves the caller Context unchanged.
func RestoreTraceContext(ctx context.Context, parent, state string) context.Context {
	if parent == "" || len(parent) > 55 || len(state) > 512 {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": parent, "tracestate": state}
	return propagation.TraceContext{}.Extract(ctx, carrier)
}
