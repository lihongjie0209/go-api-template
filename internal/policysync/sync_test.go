package policysync

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type failingPublishClient struct{ redis.UniversalClient }

type recordingSubscribeClient struct {
	redis.UniversalClient
	subscriptions chan *redis.PubSub
}

func (c *recordingSubscribeClient) Subscribe(ctx context.Context, channels ...string) *redis.PubSub {
	subscription := c.UniversalClient.Subscribe(ctx, channels...)
	c.subscriptions <- subscription
	return subscription
}

func (failingPublishClient) Publish(ctx context.Context, _ string, _ any) *redis.IntCmd {
	command := redis.NewIntCmd(ctx)
	command.SetErr(errors.New("redis password=secret-value"))
	return command
}

func TestPolicySyncFailureLogsDoNotExposeDependencyDetails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(context.Context, *slog.Logger) error
		code string
	}{
		{
			name: "revision polling",
			run: func(ctx context.Context, logger *slog.Logger) error {
				syncer := New(nil, "policies", "data_permission", time.Second, time.Second,
					func(context.Context) (string, error) { return "", errors.New("database password=secret-value") },
					func(context.Context) error { return nil }, logger, nil)
				syncer.poll(ctx)
				return nil
			},
			code: "policy_revision_read_failed",
		},
		{
			name: "snapshot refresh",
			run: func(ctx context.Context, logger *slog.Logger) error {
				syncer := New(nil, "policies", "data_permission", time.Second, time.Second,
					func(context.Context) (string, error) { return "revision-1", nil },
					func(context.Context) error { return errors.New("database password=secret-value") }, logger, nil)
				syncer.refreshAndLog(ctx, "revision")
				return nil
			},
			code: "policy_snapshot_refresh_failed",
		},
		{
			name: "redis publication",
			run: func(ctx context.Context, logger *slog.Logger) error {
				syncer := New(failingPublishClient{}, "policies", "data_permission", time.Second, time.Second,
					func(context.Context) (string, error) { return "revision-1", nil },
					func(context.Context) error { return nil }, logger, nil)
				return syncer.Changed(ctx)
			},
			code: "policy_invalidation_publish_failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			if err := test.run(t.Context(), logger); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "secret-value") || !strings.Contains(output.String(), test.code) {
				t.Fatalf("policy sync log leaked dependency details or omitted safe code: %s", output.String())
			}
		})
	}
}

func TestPollingShutdownCancellationDoesNotInvalidateSnapshot(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	var invalidations atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())
	syncer := New(nil, "policies", "data_permission", time.Second, time.Second,
		func(context.Context) (string, error) {
			cancel()
			return "", context.Canceled
		},
		func(context.Context) error { return nil }, logger, nil)
	syncer.SetFailureHandler(func() { invalidations.Add(1) })
	syncer.poll(ctx)
	if invalidations.Load() != 0 || output.Len() != 0 {
		t.Fatalf("shutdown cancellation invalidated=%d log=%q", invalidations.Load(), output.String())
	}
}

func TestPollingRevisionDeadlineInvalidatesSnapshot(t *testing.T) {
	t.Parallel()
	var invalidations atomic.Int64
	syncer := New(nil, "policies", "data_permission", time.Second, time.Second,
		func(context.Context) (string, error) { return "", context.DeadlineExceeded },
		func(context.Context) error { return nil }, slog.Default(), nil)
	syncer.SetFailureHandler(func() { invalidations.Add(1) })
	syncer.poll(t.Context())
	if invalidations.Load() != 1 {
		t.Fatalf("dependency deadline invalidations=%d, want 1", invalidations.Load())
	}
}

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
	syncer := New(nil, "", "data_permission", 10*time.Millisecond, time.Second, func(context.Context) (string, error) {
		revisionMu.Lock()
		defer revisionMu.Unlock()
		return revision, nil
	}, func(context.Context) error {
		refreshes.Add(1)
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, syncer.Refresh(t.Context()))
	require.Equal(t, "revision-1", syncer.Revision())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); syncer.Run(ctx) }()
	revisionMu.Lock()
	revision = "revision-2"
	revisionMu.Unlock()
	require.Eventually(t, func() bool { return refreshes.Load() >= 2 }, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return syncer.Revision() == "revision-2" }, time.Second, 10*time.Millisecond)
	cancel()
	<-done
}

func TestRunChecksRevisionImmediatelyAfterStartup(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	revision := "revision-1"
	var refreshes atomic.Int64
	syncer := New(nil, "", "data_permission", time.Minute, time.Second,
		func(context.Context) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			return revision, nil
		},
		func(context.Context) error { refreshes.Add(1); return nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, syncer.Refresh(t.Context()))
	mu.Lock()
	revision = "revision-2"
	mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); syncer.Run(ctx) }()
	defer func() { cancel(); <-done }()
	require.Eventually(t, func() bool { return refreshes.Load() >= 2 && syncer.Revision() == "revision-2" }, time.Second, 10*time.Millisecond)
}

func TestRunPollsAuthoritativeRevisionWhenInitialSubscriptionFails(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	address := server.Addr()
	server.Close()
	client := redis.NewClient(&redis.Options{Addr: address, DialTimeout: 10 * time.Millisecond, MaxRetries: 0})
	t.Cleanup(func() { _ = client.Close() })
	var revision atomic.Value
	revision.Store("revision-1")
	var refreshes atomic.Int64
	var logs bytes.Buffer
	syncer := New(client, "policies", "data_permission", time.Minute, 100*time.Millisecond,
		func(context.Context) (string, error) { return revision.Load().(string), nil },
		func(context.Context) error { refreshes.Add(1); return nil },
		slog.New(slog.NewJSONHandler(&logs, nil)), nil)
	require.NoError(t, syncer.Refresh(t.Context()))
	revision.Store("revision-2")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); syncer.Run(ctx) }()
	defer func() { cancel(); <-done }()
	require.Eventually(t, func() bool { return refreshes.Load() >= 2 && syncer.Revision() == "revision-2" }, time.Second, 10*time.Millisecond)
	cancel()
	<-done
	if !strings.Contains(logs.String(), "policy_subscription_failed") || strings.Contains(logs.String(), address) {
		t.Fatalf("subscription failure log exposed address or omitted reason: %s", logs.String())
	}
}

func TestRunResubscribesAfterPubSubChannelCloses(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	base := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = base.Close() })
	client := &recordingSubscribeClient{UniversalClient: base, subscriptions: make(chan *redis.PubSub, 2)}
	var refreshes atomic.Int64
	syncer := New(client, "policies", "data_permission", 10*time.Millisecond, time.Second,
		func(context.Context) (string, error) { return "revision-1", nil },
		func(context.Context) error { refreshes.Add(1); return nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, syncer.Refresh(t.Context()))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); syncer.Run(ctx) }()
	defer func() { cancel(); <-done }()
	var first *redis.PubSub
	select {
	case first = <-client.subscriptions:
	case <-time.After(time.Second):
		t.Fatal("initial subscription was not created")
	}
	require.NoError(t, first.Close())
	select {
	case <-client.subscriptions:
	case <-time.After(time.Second):
		t.Fatal("closed subscription was not replaced")
	}
	require.Eventually(t, func() bool {
		if err := base.Publish(t.Context(), "policies", "changed").Err(); err != nil {
			return false
		}
		return refreshes.Load() >= 2
	}, time.Second, 10*time.Millisecond)
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
	syncer := New(client, "policies", "data_permission", time.Second, time.Second, func(context.Context) (string, error) {
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

func TestChangedPublishesHintEvenWhenLocalRefreshFails(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	subscriber := client.Subscribe(t.Context(), "policies")
	t.Cleanup(func() { _ = subscriber.Close() })
	_, err := subscriber.Receive(t.Context())
	require.NoError(t, err)
	want := errors.New("local policy refresh unavailable")
	syncer := New(client, "policies", "data_permission", time.Second, time.Second,
		func(context.Context) (string, error) { return "revision-2", nil },
		func(context.Context) error { return want }, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.ErrorIs(t, syncer.Changed(t.Context()), want)
	message, err := subscriber.ReceiveMessage(t.Context())
	require.NoError(t, err)
	require.Equal(t, "changed", message.Payload)
}

func TestSnapshotRefreshFailureCannotInvalidateNewerSuccess(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	var attempts atomic.Int64
	var invalidations atomic.Int64
	syncer := NewSnapshot(nil, "", "data_permission", time.Second, time.Second,
		func(context.Context) (string, error) { return "current", nil },
		func(context.Context) (string, error) {
			if attempts.Add(1) == 1 {
				close(started)
				<-release
				return "", errors.New("refresh failed")
			}
			return "revision-2", nil
		}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	syncer.SetFailureHandler(func() { invalidations.Add(1) })
	failed := make(chan error, 1)
	go func() { failed <- syncer.Refresh(t.Context()) }()
	<-started
	succeeded := make(chan error, 1)
	go func() { succeeded <- syncer.Refresh(t.Context()) }()
	close(release)
	require.Error(t, <-failed)
	require.NoError(t, <-succeeded)
	require.Equal(t, int64(1), invalidations.Load())
	require.Equal(t, "revision-2", syncer.Revision())
}

func TestPollRetriesInvalidatedSnapshotWithoutRevisionChange(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int64
	var invalidations atomic.Int64
	syncer := NewSnapshot(nil, "", "data_permission", time.Second, time.Second,
		func(context.Context) (string, error) { return "revision-1", nil },
		func(context.Context) (string, error) {
			if attempts.Add(1) == 2 {
				return "", errors.New("temporary refresh failure")
			}
			return "revision-1", nil
		}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	syncer.SetFailureHandler(func() { invalidations.Add(1) })
	require.NoError(t, syncer.Refresh(t.Context()))
	require.Error(t, syncer.Refresh(t.Context()))
	require.Equal(t, int64(1), invalidations.Load())
	syncer.poll(t.Context())
	require.Equal(t, int64(3), attempts.Load())
	require.Equal(t, "revision-1", syncer.Revision())
	syncer.poll(t.Context())
	require.Equal(t, int64(3), attempts.Load())
}

func TestRevisionReadFailureInvalidatesAndRecoversSameRevision(t *testing.T) {
	t.Parallel()
	var failRead atomic.Bool
	var invalidations atomic.Int64
	var refreshes atomic.Int64
	syncer := NewSnapshot(nil, "", "data_permission", time.Second, time.Second,
		func(context.Context) (string, error) {
			if failRead.Load() {
				return "", errors.New("temporary revision read failure")
			}
			return "revision-1", nil
		},
		func(context.Context) (string, error) {
			refreshes.Add(1)
			return "revision-1", nil
		}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	syncer.SetFailureHandler(func() { invalidations.Add(1) })
	require.NoError(t, syncer.Refresh(t.Context()))
	failRead.Store(true)
	syncer.poll(t.Context())
	require.Equal(t, int64(1), invalidations.Load())
	failRead.Store(false)
	syncer.poll(t.Context())
	require.Equal(t, int64(2), refreshes.Load())
	syncer.poll(t.Context())
	require.Equal(t, int64(2), refreshes.Load())
}

func TestChangedDoesNotFailCommittedMutationWhenRedisIsUnavailable(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), DialTimeout: time.Millisecond, ReadTimeout: time.Millisecond, WriteTimeout: time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	server.Close()
	syncer := New(client, "policies", "data_permission", time.Second, 50*time.Millisecond, func(context.Context) (string, error) {
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
	syncer := New(client, "policies", "data_permission", time.Second, time.Second, func(context.Context) (string, error) {
		return "revision-1", nil
	}, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics)
	require.NoError(t, syncer.Changed(t.Context()))
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	require.ElementsMatch(t, []string{"policy_sync:data_permission:refresh:success", "policy_sync:data_permission:publish:success"}, metrics.entries)
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
	publisher := New(client, "policies", "data_permission", time.Hour, time.Second, revision, func(context.Context) error {
		publisherSnapshot.Store(databaseRevision.Load().(string))
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	subscriber := New(client, "policies", "data_permission", time.Hour, time.Second, revision, func(context.Context) error {
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
