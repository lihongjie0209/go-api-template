package observability

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/trace"
)

var errUnclassifiedFailure = errors.New("operation_failed")

// SafeError reduces an untrusted dependency error to a stable reason for
// telemetry. The fallback must be a fixed component-owned sentinel, never a
// wrapped upstream error. The original error remains available to its caller,
// but must not be copied into exported traces or logs.
func SafeError(err, fallback error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if fallback == nil {
		return errUnclassifiedFailure
	}
	return fallback
}

// RecordSafeError records only a classified error in an OpenTelemetry span.
func RecordSafeError(span trace.Span, err, fallback error) {
	if err != nil {
		span.RecordError(SafeError(err, fallback))
	}
}
