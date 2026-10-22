package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

type leaseLossLocker struct {
	mutex Lock
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

type releaseErrorMutex struct{ unlockErr error }

func (m *releaseErrorMutex) Extend(context.Context) error { return nil }
func (m *releaseErrorMutex) Unlock(context.Context) error { return m.unlockErr }
func (m *releaseErrorMutex) Until() time.Time             { return time.Now().Add(time.Minute) }

func TestWithCommittedLockDoesNotReportReleaseFailureAsMutationFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("redis release unavailable")
	called := false
	err := WithCommittedLock(t.Context(), leaseLossLocker{mutex: &releaseErrorMutex{unlockErr: want}}, "role:1", time.Second, time.Millisecond, func(context.Context) error {
		called = true
		return nil // The caller's durable transaction has committed.
	})
	if err != nil || !called {
		t.Fatalf("committed callback reported failure: called=%t err=%v", called, err)
	}
}

func TestWithCommittedLockPreservesCallbackFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("transaction rolled back")
	releaseErr := errors.New("redis release unavailable")
	err := WithCommittedLock(t.Context(), leaseLossLocker{mutex: &releaseErrorMutex{unlockErr: releaseErr}}, "role:1", time.Second, time.Millisecond, func(context.Context) error {
		return want
	})
	if !errors.Is(err, want) || !errors.Is(err, releaseErr) {
		t.Fatalf("pre-commit callback errors not preserved: %v", err)
	}
}

func TestWithCommittedLockCancelsUncommittedWorkOnLeaseLoss(t *testing.T) {
	t.Parallel()
	err := WithCommittedLock(t.Context(), leaseLossLocker{mutex: &leaseLossMutex{}}, "role:1", 30*time.Millisecond, time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return context.Cause(ctx)
	})
	if !errors.Is(err, ErrLockLost) {
		t.Fatalf("uncommitted lease loss was suppressed: %v", err)
	}
}
