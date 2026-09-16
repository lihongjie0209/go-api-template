package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

type leaseLossLocker struct {
	mutex *leaseLossMutex
}

func (l leaseLossLocker) TryLock(context.Context, string, time.Duration) (Lock, bool, error) {
	return l.mutex, true, nil
}

func (l leaseLossLocker) Lock(context.Context, string, time.Duration, time.Duration) (Lock, error) {
	return l.mutex, nil
}

type leaseLossMutex struct{}

func (*leaseLossMutex) Extend(context.Context) error { return ErrLockLost }
func (*leaseLossMutex) Unlock(context.Context) error { return nil }
func (*leaseLossMutex) Until() time.Time             { return time.Now().Add(time.Second) }

func TestWithLockCancelsCallbackWhenLeaseIsLost(t *testing.T) {
	t.Parallel()
	var callbackCause error
	err := WithLock(t.Context(), leaseLossLocker{mutex: &leaseLossMutex{}}, "resource:1", 30*time.Millisecond, time.Millisecond, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			callbackCause = context.Cause(ctx)
			return callbackCause
		case <-time.After(time.Second):
			t.Fatal("callback was not canceled after lease loss")
			return nil
		}
	})
	if !errors.Is(callbackCause, ErrLockLost) {
		t.Fatalf("callback cancellation cause = %v", callbackCause)
	}
	if !errors.Is(err, ErrLockLost) {
		t.Fatalf("WithLock() error = %v", err)
	}
}
