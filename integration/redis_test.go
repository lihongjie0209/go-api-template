//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	rediscontainer "github.com/testcontainers/testcontainers-go/modules/redis"
)

func TestRedisLockAndIdempotency(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	container, err := rediscontainer.Run(ctx, "redis:7.4-alpine")
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	connectionString, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	options, err := goredis.ParseURL(connectionString)
	if err != nil {
		t.Fatal(err)
	}
	client := goredis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })

	locker := cache.NewLocker(client)
	lock, acquired, err := locker.TryLock(ctx, "integration", 10*time.Second)
	if err != nil || !acquired {
		t.Fatalf("first lock acquired=%v err=%v", acquired, err)
	}
	_, secondAcquired, err := locker.TryLock(ctx, "integration", 10*time.Second)
	if err != nil || secondAcquired {
		t.Fatalf("competing lock acquired=%v err=%v", secondAcquired, err)
	}
	if err := lock.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	store := cache.NewRedisStore(client, cache.WithKeyPrefix("integration:"))
	if err := cache.SetJSON(ctx, store, "cache:item", map[string]string{"id": "42"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	cached, err := cache.GetJSON[map[string]string](ctx, store, "cache:item")
	if err != nil || cached["id"] != "42" {
		t.Fatalf("cached=%v err=%v", cached, err)
	}
	if err := store.Delete(ctx, "cache:item"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "cache:item"); err == nil {
		t.Fatal("deleted cache key still exists")
	}

	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- cache.WithLock(ctx, locker, "renewed", 300*time.Millisecond, 20*time.Millisecond, func(lockCtx context.Context) error {
			close(started)
			select {
			case <-lockCtx.Done():
				return context.Cause(lockCtx)
			case <-time.After(650 * time.Millisecond):
				return nil
			}
		})
	}()
	<-started
	time.Sleep(450 * time.Millisecond)
	_, acquired, err = locker.TryLock(ctx, "renewed", time.Second)
	if err != nil || acquired {
		t.Fatalf("renewed lock contention acquired=%v err=%v", acquired, err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}

	manager := idempotency.New(client, config.Config{Idempotency: config.Idempotency{Enabled: true, ProcessingTTL: time.Minute, ResultTTL: time.Hour, FailureTTL: time.Minute}}, nil)
	first, err := manager.Begin(ctx, "request-0001", "fingerprint-a")
	if err != nil || first.State != idempotency.StateAcquired {
		t.Fatalf("begin = %+v, %v", first, err)
	}
	processing, err := manager.Begin(ctx, "request-0001", "fingerprint-a")
	if err != nil || processing.State != idempotency.StateProcessing {
		t.Fatalf("processing = %+v, %v", processing, err)
	}
	conflict, err := manager.Begin(ctx, "request-0001", "fingerprint-b")
	if err != nil || conflict.State != idempotency.StateConflict {
		t.Fatalf("conflict = %+v, %v", conflict, err)
	}
	response := map[string]string{"id": "user-1"}
	if err := manager.Complete(ctx, "request-0001", first.Owner, response); err != nil {
		t.Fatal(err)
	}
	replay, err := manager.Begin(ctx, "request-0001", "fingerprint-a")
	if err != nil || replay.State != idempotency.StateCompleted {
		t.Fatalf("replay = %+v, %v", replay, err)
	}

	leaseManager := idempotency.New(client, config.Config{Idempotency: config.Idempotency{Enabled: true, ProcessingTTL: 300 * time.Millisecond, ResultTTL: time.Hour, FailureTTL: time.Minute, MaxResponseBytes: 1024}}, nil)
	leased, err := leaseManager.Begin(ctx, "request-lease", "fingerprint-a")
	if err != nil || leased.State != idempotency.StateAcquired {
		t.Fatalf("lease begin = %+v, %v", leased, err)
	}
	leaseCtx, stopLease, err := leaseManager.StartLease(ctx, "request-lease", leased.Owner)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(220 * time.Millisecond)
	if err := leaseCtx.Err(); err != nil {
		t.Fatalf("renewed idempotency lease canceled: %v", err)
	}
	if err := stopLease(); err != nil {
		t.Fatal(err)
	}
	if err := leaseManager.Abort(ctx, "request-lease", leased.Owner); err != nil {
		t.Fatal(err)
	}
	retried, err := leaseManager.Begin(ctx, "request-lease", "fingerprint-a")
	if err != nil || retried.State != idempotency.StateAcquired {
		t.Fatalf("lease retry = %+v, %v", retried, err)
	}
}
