package cache

import (
	"context"
	"testing"
	"time"
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
