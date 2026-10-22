package scheduler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/robfig/cron/v3"
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

type releaseTrackingMutex struct{ released *bool }

func (releaseTrackingMutex) Extend(context.Context) error { return nil }
func (m releaseTrackingMutex) Unlock(context.Context) error {
	*m.released = true
	return nil
}
func (releaseTrackingMutex) Until() time.Time { return time.Now().Add(time.Minute) }

type contendingLocker struct {
	mutex      cache.Lock
	contention int32
	attempts   atomic.Int32
	cancel     context.CancelFunc
}

func (l *contendingLocker) TryLock(context.Context, string, time.Duration) (cache.Lock, bool, error) {
	if l.attempts.Add(1) <= l.contention {
		if l.cancel != nil {
			l.cancel()
		}
		return nil, false, nil
	}
	return l.mutex, true, nil
}

func (*contendingLocker) Lock(context.Context, string, time.Duration, time.Duration) (cache.Lock, error) {
	return nil, errors.New("unexpected blocking acquisition")
}

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

func TestRunJobRejectsSuccessAfterContextCancellation(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := runJob(parent, "orders-service", "reconcile", time.Minute, time.Second, lockerStub{mutex: mutexStub{}, acquired: true}, func(context.Context) error {
		cancel()
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runJob() error = %v, want context cancellation", err)
	}
}

func TestRunJobRejectsSuccessAfterDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		err := runJob(t.Context(), "orders-service", "reconcile", time.Second, 10*time.Second, lockerStub{mutex: mutexStub{}, acquired: true}, func(context.Context) error {
			time.Sleep(2 * time.Second)
			return nil
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("runJob() error = %v, want deadline exceeded", err)
		}
	})
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

func TestSchedulerOperationalLogsDoNotExposeDependencyErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		code string
		run  func(*slog.Logger)
	}{
		{
			name: "database", code: "scheduler_operation_failed",
			run: func(logger *slog.Logger) {
				manager := &Manager{logger: logger}
				manager.logOperationFailure(t.Context(), "claim manual scheduled job", errors.New("database URL with secret-value"))
			},
		},
		{
			name: "consumer", code: "scheduler_consumer_failed",
			run: func(logger *slog.Logger) {
				logSchedulerConsumerFailure(logger, errors.New("queue URL with secret-value"))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			test.run(slog.New(slog.NewJSONHandler(&output, nil)))
			if bytes.Contains(output.Bytes(), []byte("secret-value")) || !bytes.Contains(output.Bytes(), []byte(test.code)) {
				t.Fatalf("scheduler log leaked dependency error or omitted safe code: %s", output.String())
			}
		})
	}
}

func TestCronRecoveryDoesNotLogPanicValueOrStack(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	job := cron.NewChain(cron.Recover(safeCronLogger{logger})).Then(cron.FuncJob(func() {
		panic("secret-value")
	}))
	job.Run()
	if bytes.Contains(output.Bytes(), []byte("secret-value")) || bytes.Contains(output.Bytes(), []byte("stack")) || !bytes.Contains(output.Bytes(), []byte("job_panic")) {
		t.Fatalf("cron recovery log exposed panic content or omitted safe reason: %s", output.String())
	}
}

func TestRunJobRecoversHandlerPanicAndReleasesLock(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	released := false
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	err := runJob(t.Context(), "orders-service", "reconcile", time.Second, time.Second, lockerStub{mutex: releaseTrackingMutex{released: &released}, acquired: true}, func(context.Context) error {
		panic("secret-value")
	}, logger)
	if !errors.Is(err, ErrJobPanicked) || !released {
		t.Fatalf("runJob() error=%v released=%t, want recovered panic and released lock", err, released)
	}
	if bytes.Contains(output.Bytes(), []byte("secret-value")) || !bytes.Contains(output.Bytes(), []byte("job_panic")) {
		t.Fatalf("scheduler log exposed panic value or omitted safe reason: %s", output.String())
	}
}

func TestManualJobRetriesLockContentionWithinDeadline(t *testing.T) {
	t.Parallel()
	locker := &contendingLocker{mutex: mutexStub{}, contention: 1}
	called := 0
	err := runManualJobContext(t.Context(), "reconcile", time.Second, time.Millisecond, locker, func(context.Context) error {
		called++
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || locker.attempts.Load() != 2 || called != 1 {
		t.Fatalf("manual lock retry error=%v attempts=%d called=%d", err, locker.attempts.Load(), called)
	}
}

func TestManualJobStopsRetryingWhenDeadlineContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	locker := &contendingLocker{contention: 100, cancel: cancel}
	called := false
	err := runManualJobContext(ctx, "reconcile", time.Second, time.Second, locker, func(context.Context) error {
		called = true
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, context.Canceled) || locker.attempts.Load() != 1 || called {
		t.Fatalf("manual retry cancellation error=%v attempts=%d called=%t", err, locker.attempts.Load(), called)
	}
}

func TestManualJobLockContentionUntilDeadlineIsError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		locker := &contendingLocker{contention: 100}
		called := false
		err := runManualJobContext(ctx, "reconcile", time.Second, 100*time.Millisecond, locker, func(context.Context) error {
			called = true
			return nil
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if !errors.Is(err, context.DeadlineExceeded) || statusForRun(err) != "error" || called {
			t.Fatalf("manual lock deadline error=%v status=%q called=%t", err, statusForRun(err), called)
		}
		if locker.attempts.Load() < 2 {
			t.Fatalf("manual lock attempts=%d, want retry before deadline", locker.attempts.Load())
		}
	})
}
