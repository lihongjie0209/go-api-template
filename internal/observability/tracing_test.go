package observability

import (
	"slices"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"go.opentelemetry.io/otel/attribute"
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

func TestTracePropagatorIncludesW3CTraceAndBaggage(t *testing.T) {
	t.Parallel()
	fields := tracePropagator().Fields()
	for _, field := range []string{"traceparent", "tracestate", "baggage"} {
		if !slices.Contains(fields, field) {
			t.Errorf("propagator fields = %v, missing %q", fields, field)
		}
	}
}
