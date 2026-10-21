package observability

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/trace"
)

var errUnclassifiedFailure = errors.New("operation_failed")

type safeFailure struct {
	reason string
	class  error
}

func (e safeFailure) Error() string { return e.reason }
func (e safeFailure) Unwrap() error { return e.class }

// SafeReason returns a frozen reason only for values produced by SafeError or
// the two well-known Context cancellation classes. It deliberately does not
// unwrap arbitrary errors because their outer text may contain secrets.
func SafeReason(err error) (string, bool) {
	switch err {
	case context.Canceled:
		return context.Canceled.Error(), true
	case context.DeadlineExceeded:
		return context.DeadlineExceeded.Error(), true
	case errUnclassifiedFailure:
		return errUnclassifiedFailure.Error(), true
	}
	classified, ok := err.(safeFailure)
	if !ok {
		return "", false
	}
	return classified.reason, true
}

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
	reason := fallback.Error()
	if !validFailureReason(reason) {
		return errUnclassifiedFailure
	}
	return safeFailure{reason: reason, class: fallback}
}

func validFailureReason(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

// RecordSafeError records only a classified error in an OpenTelemetry span.
func RecordSafeError(span trace.Span, err, fallback error) {
	if err != nil {
		span.RecordError(SafeError(err, fallback))
	}
}
