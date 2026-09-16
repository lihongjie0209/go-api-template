package policysync

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type metricsStub struct {
	mu      sync.Mutex
	entries []string
}

func (m *metricsStub) ObserveInfrastructure(component, backend, operation, status string, _ time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, component+":"+backend+":"+operation+":"+status)
}

func TestPollingRecoversLostNotification(t *testing.T) {
	t.Parallel()
	var refreshes atomic.Int64
	var revisionMu sync.Mutex
	revision := "revision-1"
	syncer := New(nil, "", "pbac", 10*time.Millisecond, time.Second, func(context.Context) (string, error) {
		revisionMu.Lock()
		defer revisionMu.Unlock()
		return revision, nil
	}, func(context.Context) error {
		refreshes.Add(1)
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, syncer.Refresh(t.Context()))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); syncer.Run(ctx) }()
	revisionMu.Lock()
	revision = "revision-2"
	revisionMu.Unlock()
	require.Eventually(t, func() bool { return refreshes.Load() >= 2 }, time.Second, 10*time.Millisecond)
	cancel()
	<-done
}

func TestChangedRefreshesAndPublishesHint(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	subscriber := client.Subscribe(t.Context(), "policies")
	t.Cleanup(func() { _ = subscriber.Close() })
	_, err := subscriber.Receive(t.Context())
	require.NoError(t, err)

	var refreshes atomic.Int64
	syncer := New(client, "policies", "pbac", time.Second, time.Second, func(context.Context) (string, error) {
		return "revision-1", nil
	}, func(context.Context) error {
		refreshes.Add(1)
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, syncer.Changed(t.Context()))
	require.Equal(t, int64(1), refreshes.Load())
	message, err := subscriber.ReceiveMessage(t.Context())
	require.NoError(t, err)
	require.Equal(t, "changed", message.Payload)
}

func TestChangedDoesNotFailCommittedMutationWhenRedisIsUnavailable(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), DialTimeout: time.Millisecond, ReadTimeout: time.Millisecond, WriteTimeout: time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	server.Close()
	syncer := New(client, "policies", "pbac", time.Second, 50*time.Millisecond, func(context.Context) (string, error) {
		return "revision-1", nil
	}, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, syncer.Changed(t.Context()))
}

func TestChangedObservesRefreshAndPublish(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	metrics := &metricsStub{}
	syncer := New(client, "policies", "pbac", time.Second, time.Second, func(context.Context) (string, error) {
		return "revision-1", nil
	}, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics)
	require.NoError(t, syncer.Changed(t.Context()))
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	require.ElementsMatch(t, []string{"policy_sync:pbac:refresh:success", "policy_sync:pbac:publish:success"}, metrics.entries)
}

func TestNotificationConvergesAnotherInstance(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	var databaseRevision atomic.Value
	databaseRevision.Store("revision-1")
	var publisherSnapshot atomic.Value
	publisherSnapshot.Store("")
	var subscriberSnapshot atomic.Value
	subscriberSnapshot.Store("")
	revision := func(context.Context) (string, error) {
		return databaseRevision.Load().(string), nil
	}
	publisher := New(client, "policies", "pbac", time.Hour, time.Second, revision, func(context.Context) error {
		publisherSnapshot.Store(databaseRevision.Load().(string))
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	subscriber := New(client, "policies", "pbac", time.Hour, time.Second, revision, func(context.Context) error {
		subscriberSnapshot.Store(databaseRevision.Load().(string))
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, publisher.Refresh(t.Context()))
	require.NoError(t, subscriber.Refresh(t.Context()))

	runCtx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		subscriber.Run(runCtx)
	}()
	require.Eventually(t, func() bool {
		counts, err := client.PubSubNumSub(t.Context(), "policies").Result()
		return err == nil && counts["policies"] == 1
	}, time.Second, 10*time.Millisecond)

	databaseRevision.Store("revision-2")
	require.NoError(t, publisher.Changed(t.Context()))
	require.Eventually(t, func() bool {
		return subscriberSnapshot.Load().(string) == "revision-2"
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, "revision-2", publisherSnapshot.Load())
	cancel()
	<-done
}
