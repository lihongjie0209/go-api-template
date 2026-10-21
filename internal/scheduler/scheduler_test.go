package scheduler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type lockerStub struct {
	mutex    cache.Lock
	acquired bool
	err      error
}

func (l lockerStub) TryLock(context.Context, string, time.Duration) (cache.Lock, bool, error) {
	return l.mutex, l.acquired, l.err
}

func (lockerStub) Lock(context.Context, string, time.Duration, time.Duration) (cache.Lock, error) {
	return nil, errors.New("unexpected blocking acquisition")
}

type mutexStub struct{}

func (mutexStub) Extend(context.Context) error { return nil }
func (mutexStub) Unlock(context.Context) error { return nil }
func (mutexStub) Until() time.Time             { return time.Now().Add(time.Minute) }

func TestRunSampleFailsWhenDistributedLockIsUnavailable(t *testing.T) {
	t.Parallel()
	err := runSample(t.Context(), "orders-service", time.Second, time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, cache.ErrInvalidLock) {
		t.Fatalf("runSample() error=%v", err)
	}
}

func TestRunSampleSkipsDistributedContention(t *testing.T) {
	t.Parallel()
	err := runSample(t.Context(), "orders-service", time.Second, time.Second, lockerStub{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("runSample() error=%v", err)
	}
}

func TestRunSampleExecutesWithAcquiredLock(t *testing.T) {
	t.Parallel()
	// The sample callback is intentionally tiny; this assertion guards that the
	// shared lease lifecycle can execute and release an acquired task.
	err := runSample(t.Context(), "orders-service", time.Second, time.Second, lockerStub{mutex: mutexStub{}, acquired: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("runSample() error=%v", err)
	}
}

func TestRunJobInjectsSystemPrincipalRequestIDAndDeadline(t *testing.T) {
	t.Parallel()
	called := false
	err := runJob(t.Context(), "orders-service", "reconcile", time.Second, time.Second, lockerStub{mutex: mutexStub{}, acquired: true}, func(ctx context.Context) error {
		called = true
		principal, ok := platformprincipal.FromContext(ctx)
		if !ok || principal.ID != "orders-service:scheduler:reconcile" || principal.Type != platformprincipal.TypeSystem {
			t.Fatalf("principal=%+v ok=%t", principal, ok)
		}
		if id, ok := requestid.FromContext(ctx); !ok || id == "" {
			t.Fatalf("request ID=%q ok=%t", id, ok)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("job context has no deadline")
		}
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || !called {
		t.Fatalf("runJob() called=%t error=%v", called, err)
	}
}

func TestRunJobHonorsSchedulerShutdown(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancel(t.Context())
	cancel()
	err := runJob(parent, "orders-service", "reconcile", time.Hour, time.Second, lockerStub{mutex: mutexStub{}, acquired: true}, func(ctx context.Context) error {
		return ctx.Err()
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runJob() error = %v, want context cancellation", err)
	}
}

func TestRunJobDoesNotLogHandlerErrorDetails(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	secretErr := errors.New("upstream request failed with secret-value")
	err := runJob(t.Context(), "orders-service", "reconcile", time.Second, time.Second, lockerStub{mutex: mutexStub{}, acquired: true}, func(context.Context) error {
		return secretErr
	}, logger)
	if !errors.Is(err, secretErr) {
		t.Fatalf("runJob() error=%v, want original handler error for trusted caller", err)
	}
	if bytes.Contains(output.Bytes(), []byte("secret-value")) || !bytes.Contains(output.Bytes(), []byte("scheduled_job_failed")) {
		t.Fatalf("scheduler log exposes handler error or omits safe code: %s", output.String())
	}
}

func TestRecordJobFailureDoesNotExposeHandlerErrorInTrace(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	_, span := provider.Tracer("scheduler-test").Start(t.Context(), "cron.reconcile")
	recordJobFailure(span, errors.New("upstream request failed with secret-value"))
	span.End()
	spans := recorder.Ended()
	if len(spans) != 1 || len(spans[0].Events()) != 1 {
		t.Fatalf("recorded spans=%d events=%v", len(spans), spans)
	}
	foundSafeCode := false
	for _, attribute := range spans[0].Events()[0].Attributes {
		if bytes.Contains([]byte(attribute.Value.AsString()), []byte("secret-value")) {
			t.Fatalf("trace event exposes handler error: %v", attribute)
		}
		if attribute.Key == "exception.message" && attribute.Value.AsString() == "scheduled_job_failed" {
			foundSafeCode = true
		}
	}
	if !foundSafeCode {
		t.Fatalf("trace event lacks safe failure code: %v", spans[0].Events()[0].Attributes)
	}
}
