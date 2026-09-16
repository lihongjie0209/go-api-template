package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
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
