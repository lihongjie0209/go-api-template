package observability

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type mutableFailureReason struct{ value string }

func (e *mutableFailureReason) Error() string { return e.value }

func TestSafeErrorPreservesOnlyKnownFailureClasses(t *testing.T) {
	t.Parallel()
	fallback := errors.New("dependency_failed")
	for _, test := range []struct {
		name  string
		cause error
		want  error
	}{
		{name: "success"},
		{name: "canceled", cause: errors.Join(context.Canceled, errors.New("secret-value")), want: context.Canceled},
		{name: "deadline", cause: errors.Join(context.DeadlineExceeded, errors.New("secret-value")), want: context.DeadlineExceeded},
		{name: "dependency", cause: errors.New("upstream URL with secret-value"), want: fallback},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := SafeError(test.cause, fallback)
			if test.want == nil {
				if got != nil {
					t.Fatalf("SafeError() = %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, test.want) || strings.Contains(got.Error(), "secret-value") {
				t.Fatalf("SafeError() = %v, want %v without secret", got, test.want)
			}
		})
	}
}

func TestRecordSafeErrorOmitsDependencyDetailsFromTrace(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	_, span := provider.Tracer("observability-test").Start(t.Context(), "dependency.call")
	RecordSafeError(span, errors.New("request https://secret-value.example failed"), errors.New("dependency_failed"))
	span.End()
	spans := recorder.Ended()
	if len(spans) != 1 || len(spans[0].Events()) != 1 {
		t.Fatalf("spans=%d events=%v", len(spans), spans)
	}
	foundSafeCode := false
	for _, attribute := range spans[0].Events()[0].Attributes {
		message := attribute.Value.AsString()
		if strings.Contains(message, "secret-value") {
			t.Fatalf("trace event leaked dependency detail: %v", attribute)
		}
		if attribute.Key == "exception.message" && message == "dependency_failed" {
			foundSafeCode = true
		}
	}
	if !foundSafeCode {
		t.Fatalf("trace event lacks safe failure code: %v", spans[0].Events()[0].Attributes)
	}
}

func TestSafeErrorWithoutFallbackStillFailsClosed(t *testing.T) {
	t.Parallel()
	got := SafeError(errors.New("secret-value"), nil)
	if got == nil || got.Error() != "operation_failed" {
		t.Fatalf("SafeError() = %v, want fixed failure code", got)
	}
}

func TestSafeErrorRejectsUnsafeFallbackReasons(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		fallback error
	}{
		{name: "upstream error text", fallback: errors.New("postgres password=secret-value")},
		{name: "wrapped reason", fallback: errors.Join(errors.New("dependency_failed"), errors.New("secret-value"))},
		{name: "too long", fallback: errors.New(strings.Repeat("x", 65))},
		{name: "uppercase reason", fallback: errors.New("Database_Failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := SafeError(errors.New("upstream token=secret-value"), test.fallback)
			if got == nil || got.Error() != "operation_failed" {
				t.Fatalf("SafeError() = %v, want bounded generic reason", got)
			}
		})
	}
}

func TestSafeErrorFreezesValidatedFallbackReason(t *testing.T) {
	t.Parallel()
	fallback := &mutableFailureReason{value: "dependency_failed"}
	got := SafeError(errors.New("upstream failure"), fallback)
	fallback.value = "password=secret-value"
	if got.Error() != "dependency_failed" || !errors.Is(got, fallback) {
		t.Fatalf("SafeError() = %v, want immutable reason with fallback identity", got)
	}
}

func TestSafeReasonOnlyAcceptsClassifiedErrors(t *testing.T) {
	t.Parallel()
	classified := SafeError(errors.New("provider secret-value"), errors.New("dependency_failed"))
	for _, test := range []struct {
		name   string
		err    error
		reason string
		ok     bool
	}{
		{name: "classified", err: classified, reason: "dependency_failed", ok: true},
		{name: "raw error", err: errors.New("dependency_failed")},
		{name: "wrapped classified", err: errors.Join(classified, errors.New("secret-value"))},
		{name: "canceled", err: context.Canceled, reason: "context canceled", ok: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reason, ok := SafeReason(test.err)
			if reason != test.reason || ok != test.ok {
				t.Fatalf("SafeReason() = %q, %t; want %q, %t", reason, ok, test.reason, test.ok)
			}
		})
	}
}
