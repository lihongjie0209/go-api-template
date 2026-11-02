package observability

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
)

type tracingHookLifecycle struct{ hooks []fx.Hook }

func (l *tracingHookLifecycle) Append(hook fx.Hook) { l.hooks = append(l.hooks, hook) }

func TestTracingGlobalProviderFollowsFxLifecycle(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})
	lifecycle := new(tracingHookLifecycle)
	cfg := config.Config{Observability: config.Observability{TracingEnabled: true, TracingEndpoint: "http://collector.example"}}
	tracing, err := newTracingWithExporter(lifecycle, cfg, func(context.Context, string) (sdktrace.SpanExporter, error) {
		return tracetest.NewInMemoryExporter(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lifecycle.hooks) != 1 {
		t.Fatalf("tracing lifecycle hooks = %d, want 1", len(lifecycle.hooks))
	}
	if otel.GetTracerProvider() != previousProvider {
		t.Error("tracing replaced the global provider before Fx Start")
	}
	if lifecycle.hooks[0].OnStart == nil {
		t.Error("tracing has no Fx start hook")
	} else if err := lifecycle.hooks[0].OnStart(t.Context()); err != nil {
		t.Fatal(err)
	}
	if otel.GetTracerProvider() == previousProvider {
		t.Error("tracing did not install its provider at Fx Start")
	}
	if lifecycle.hooks[0].OnStop == nil {
		t.Error("tracing has no Fx stop hook")
	} else if err := lifecycle.hooks[0].OnStop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if otel.GetTracerProvider() != previousProvider {
		t.Error("tracing did not restore the previous provider at Fx Stop")
	}
	if !tracing.Enabled() {
		t.Error("tracing unexpectedly disabled")
	}
}

func TestTracingStartupErrorIsSafe(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Observability: config.Observability{TracingEnabled: true, TracingEndpoint: "https://collector.example"}}
	for _, test := range []struct {
		name   string
		create func(context.Context, string) (sdktrace.SpanExporter, error)
	}{
		{name: "exporter error", create: func(context.Context, string) (sdktrace.SpanExporter, error) {
			return nil, errors.New("collector URL contains secret-value")
		}},
		{name: "missing exporter", create: func(context.Context, string) (sdktrace.SpanExporter, error) {
			return nil, nil
		}},
		{name: "typed nil exporter", create: func(context.Context, string) (sdktrace.SpanExporter, error) {
			var exporter *tracetest.InMemoryExporter
			return exporter, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lifecycle := new(tracingHookLifecycle)
			tracing, err := newTracingWithExporter(lifecycle, cfg, test.create)
			if err != nil || tracing == nil || len(lifecycle.hooks) != 1 {
				t.Fatalf("NewTracing() = %v, %v; want registered start hook", tracing, err)
			}
			err = lifecycle.hooks[0].OnStart(t.Context())
			if !errors.Is(err, errTracingStartupFailed) || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("tracing OnStart() = %v; want safe startup failure", err)
			}
		})
	}
}

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
