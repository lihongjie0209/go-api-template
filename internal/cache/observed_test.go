package cache

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestObserveStoreWithoutMetricsPreservesStore(t *testing.T) {
	t.Parallel()

	store := &stubStore{}
	if got := ObserveStore(store, nil, "redis"); got != store {
		t.Fatalf("ObserveStore() = %T, want original store", got)
	}
}

func TestObserveLockerWithoutMetricsPreservesLocker(t *testing.T) {
	t.Parallel()

	locker := &stubLocker{}
	if got := ObserveLocker(locker, nil, "redis"); got != locker {
		t.Fatalf("ObserveLocker() = %T, want original locker", got)
	}
}

func TestObservedInfrastructureErrorsAreSafeInTraces(t *testing.T) {
	t.Parallel()
	rawError := errors.New("redis connection failed with secret-value")
	for _, test := range []struct {
		name string
		code string
		run  func(trace.Tracer) error
	}{
		{
			name: "cache", code: "cache_operation_failed",
			run: func(tracer trace.Tracer) error {
				store := &observedStore{next: &errorStore{err: rawError}, metrics: observability.NewMetrics(config.Config{}, nil, nil), backend: "redis", tracer: tracer}
				_, err := store.Get(t.Context(), "private-cache-key")
				return err
			},
		},
		{
			name: "lock", code: "lock_operation_failed",
			run: func(tracer trace.Tracer) error {
				locker := &observedLocker{next: &errorLocker{err: rawError}, metrics: observability.NewMetrics(config.Config{}, nil, nil), backend: "redis", tracer: tracer}
				_, _, err := locker.TryLock(t.Context(), "private-lock-key", time.Second)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
			if err := test.run(provider.Tracer("cache-test")); !errors.Is(err, rawError) {
				t.Fatalf("dependency error = %v, want original error", err)
			}
			spans := recorder.Ended()
			if len(spans) != 1 || len(spans[0].Events()) != 1 {
				t.Fatalf("spans=%d events=%v", len(spans), spans)
			}
			foundSafeCode := false
			for _, attr := range spans[0].Events()[0].Attributes {
				message := attr.Value.AsString()
				if strings.Contains(message, "secret-value") || strings.Contains(message, "private-") {
					t.Fatalf("trace event leaked dependency detail: %v", attr)
				}
				if attr.Key == "exception.message" && message == test.code {
					foundSafeCode = true
				}
			}
			if !foundSafeCode {
				t.Fatalf("trace event lacks %q: %v", test.code, spans[0].Events()[0].Attributes)
			}
		})
	}
}

type errorStore struct {
	stubStore
	err error
}

func (s *errorStore) Get(context.Context, string) ([]byte, error) { return nil, s.err }

type errorLocker struct {
	stubLocker
	err error
}

func (l *errorLocker) TryLock(context.Context, string, time.Duration) (Lock, bool, error) {
	return nil, false, l.err
}

type stubStore struct{}

func (*stubStore) Get(context.Context, string) ([]byte, error)              { return nil, ErrMiss }
func (*stubStore) Set(context.Context, string, []byte, time.Duration) error { return nil }
func (*stubStore) SetIfAbsent(context.Context, string, []byte, time.Duration) (bool, error) {
	return true, nil
}
func (*stubStore) Delete(context.Context, ...string) error      { return nil }
func (*stubStore) Exists(context.Context, string) (bool, error) { return false, nil }

type stubLocker struct{}

func (*stubLocker) TryLock(context.Context, string, time.Duration) (Lock, bool, error) {
	return nil, false, nil
}
func (*stubLocker) Lock(context.Context, string, time.Duration, time.Duration) (Lock, error) {
	return nil, nil
}
