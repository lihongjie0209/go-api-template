package observability

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceResourceMergesDefaultsAndServiceIdentity(t *testing.T) {
	t.Parallel()
	resource, err := traceResource(config.Config{
		Runtime: config.Runtime{ActiveProfile: "test"},
		App:     config.App{Name: "orders", Schema: "orders"},
	})
	if err != nil {
		t.Fatal(err)
	}
	set := resource.Set()
	for key, want := range map[attribute.Key]string{
		"service.name":                "orders",
		"service.namespace":           "orders",
		"deployment.environment.name": "test",
		"telemetry.sdk.language":      "go",
	} {
		value, ok := set.Value(key)
		if !ok || value.AsString() != want {
			t.Errorf("resource[%s] = %q, %v; want %q", key, value.AsString(), ok, want)
		}
	}
}

func TestDurableTraceContextRoundTripAndBounds(t *testing.T) {
	t.Parallel()
	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	state, err := trace.ParseTraceState("vendor=value")
	if err != nil {
		t.Fatal(err)
	}
	original := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, TraceState: state})
	parent, savedState := CaptureTraceContext(trace.ContextWithSpanContext(t.Context(), original))
	if parent == "" || savedState != "vendor=value" {
		t.Fatalf("captured trace context = %q, %q", parent, savedState)
	}
	restored := trace.SpanContextFromContext(RestoreTraceContext(t.Context(), parent, savedState))
	if !restored.IsRemote() || restored.TraceID() != traceID || restored.SpanID() != spanID || restored.TraceState().String() != state.String() {
		t.Fatalf("restored trace context = %v", restored)
	}
	if emptyParent, emptyState := CaptureTraceContext(t.Context()); emptyParent != "" || emptyState != "" {
		t.Fatalf("empty trace context = %q, %q", emptyParent, emptyState)
	}
	for _, test := range []struct {
		name   string
		parent string
		state  string
	}{
		{name: "malformed parent", parent: strings.Repeat("x", 55)},
		{name: "oversized parent", parent: parent + "x"},
		{name: "oversized state", parent: parent, state: strings.Repeat("x", 513)},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := RestoreTraceContext(context.Background(), test.parent, test.state)
			if trace.SpanContextFromContext(ctx).IsValid() {
				t.Fatal("invalid persisted trace context was accepted")
			}
		})
	}
}

func TestTracePropagatorIncludesW3CTraceAndBaggage(t *testing.T) {
	t.Parallel()
	fields := tracePropagator().Fields()
	for _, field := range []string{"traceparent", "tracestate", "baggage"} {
		if !slices.Contains(fields, field) {
			t.Errorf("propagator fields = %v, missing %q", fields, field)
		}
	}
}
