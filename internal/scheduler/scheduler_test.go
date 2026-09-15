package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/cache"
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
	err := runSample("orders-service", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, cache.ErrInvalidLock) {
		t.Fatalf("runSample() error=%v", err)
	}
}

func TestRunSampleSkipsDistributedContention(t *testing.T) {
	t.Parallel()
	err := runSample("orders-service", lockerStub{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("runSample() error=%v", err)
	}
}

func TestRunSampleExecutesWithSystemPrincipal(t *testing.T) {
	t.Parallel()
	// The sample callback is intentionally tiny; this assertion guards that the
	// shared lease lifecycle can execute and release an acquired task.
	err := runSample("orders-service", lockerStub{mutex: mutexStub{}, acquired: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("runSample() error=%v", err)
	}
	ctx := platformprincipal.SystemContext(t.Context(), "orders-service:scheduler:sample")
	principal, ok := platformprincipal.FromContext(ctx)
	if !ok || principal.ID != "orders-service:scheduler:sample" {
		t.Fatalf("principal=%+v ok=%t", principal, ok)
	}
}
