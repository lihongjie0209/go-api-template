package cache

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestLocker_TryLockContentionAndUnlock(t *testing.T) {
	t.Parallel()
	locker, closeRedis := newTestLocker(t)
	defer closeRedis()

	first, acquired, err := locker.TryLock(t.Context(), "job", time.Minute)
	if err != nil {
		t.Fatalf("first TryLock() error = %v", err)
	}
	if !acquired {
		t.Fatal("first TryLock() acquired = false, want true")
	}

	_, acquired, err = locker.TryLock(t.Context(), "job", time.Minute)
	if err != nil {
		t.Fatalf("second TryLock() error = %v", err)
	}
	if acquired {
		t.Fatal("second TryLock() acquired = true, want false")
	}

	if err := first.Unlock(t.Context()); err != nil {
		t.Fatalf("Unlock() error = %v", err)
	}
	second, acquired, err := locker.TryLock(t.Context(), "job", time.Minute)
	if err != nil {
		t.Fatalf("third TryLock() error = %v", err)
	}
	if !acquired {
		t.Fatal("third TryLock() acquired = false, want true")
	}
	if err := second.Unlock(t.Context()); err != nil {
		t.Fatalf("second Unlock() error = %v", err)
	}
}

func TestLock_Extend(t *testing.T) {
	t.Parallel()
	locker, closeRedis := newTestLocker(t)
	defer closeRedis()
	lock, acquired, err := locker.TryLock(t.Context(), "extend", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("TryLock() = (_, %v, %v), want acquired", acquired, err)
	}
	before := lock.Until()
	time.Sleep(time.Millisecond)
	if err := lock.Extend(t.Context()); err != nil {
		t.Fatalf("Extend() error = %v", err)
	}
	if !lock.Until().After(before) {
		t.Fatalf("Until() = %v, want after %v", lock.Until(), before)
	}
}

func TestLockReportsOwnershipLoss(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	locker := NewLocker(client)
	lock, acquired, err := locker.TryLock(t.Context(), "lost", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("TryLock acquired=%v err=%v", acquired, err)
	}
	server.Del("lock:lost")
	if err := lock.Extend(t.Context()); !errors.Is(err, ErrLockLost) {
		t.Fatalf("Extend error=%v, want ErrLockLost", err)
	}
}

func TestLocker_KeyPrefix(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	locker := NewLocker(client, WithLockKeyPrefix("billing:lock:"))

	lock, acquired, err := locker.TryLock(t.Context(), "invoice:1", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("TryLock() = (_, %v, %v), want acquired", acquired, err)
	}
	if !server.Exists("billing:lock:invoice:1") {
		t.Fatal("TryLock() did not use the configured namespace")
	}
	if err := lock.Unlock(t.Context()); err != nil {
		t.Fatalf("Unlock() error = %v", err)
	}
}

func TestLocker_Validation(t *testing.T) {
	t.Parallel()
	locker, closeRedis := newTestLocker(t)
	defer closeRedis()
	tests := []struct {
		name string
		key  string
		ttl  time.Duration
	}{{name: "empty key", ttl: time.Second}, {name: "invalid ttl", key: "key"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := locker.TryLock(t.Context(), tt.key, tt.ttl)
			if err == nil {
				t.Fatal("TryLock() error = nil, want error")
			}
		})
	}
}

func newTestLocker(t *testing.T) (Locker, func()) {
	t.Helper()
	server, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	return NewLocker(client), func() {
		if err := client.Close(); err != nil && !strings.Contains(err.Error(), "closed") {
			t.Errorf("close redis: %v", err)
		}
		server.Close()
	}
}
