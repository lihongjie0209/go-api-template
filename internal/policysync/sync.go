package policysync

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/redis/go-redis/v9"
)

const defaultPollInterval = 30 * time.Second

var (
	errPolicyInvalidationPublishFailed = errors.New("policy_invalidation_publish_failed")
	errPolicyRevisionReadFailed        = errors.New("policy_revision_read_failed")
	errPolicySnapshotRefreshFailed     = errors.New("policy_snapshot_refresh_failed")
)

// Sync keeps an immutable in-process policy snapshot aligned with its
// authoritative database state. Pub/Sub reduces propagation latency; revision
// polling is the correctness fallback because Redis notifications are lossy.
type Sync struct {
	redis            redis.UniversalClient
	channel          string
	pollInterval     time.Duration
	timeout          time.Duration
	revision         func(context.Context) (string, error)
	refresh          func(context.Context) error
	snapshotRefresh  func(context.Context) (string, error)
	onRefreshFailure func()
	logger           *slog.Logger
	component        string
	metrics          metricsObserver

	mu           sync.Mutex
	lastRevision string
	invalidated  bool
}

type metricsObserver interface {
	ObserveInfrastructure(component, backend, operation, status string, started time.Time)
}

func New(client redis.UniversalClient, channel, component string, pollInterval, timeout time.Duration, revision func(context.Context) (string, error), refresh func(context.Context) error, logger *slog.Logger, metrics metricsObserver) *Sync {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Sync{redis: client, channel: channel, component: component, pollInterval: pollInterval, timeout: timeout, revision: revision, refresh: refresh, logger: logger, metrics: metrics}
}

// NewSnapshot uses one consistent database view for policies and revision.
func NewSnapshot(client redis.UniversalClient, channel, component string, pollInterval, timeout time.Duration, revision func(context.Context) (string, error), refresh func(context.Context) (string, error), logger *slog.Logger, metrics metricsObserver) *Sync {
	s := New(client, channel, component, pollInterval, timeout, revision, nil, logger, metrics)
	s.snapshotRefresh = refresh
	return s
}

// SetFailureHandler installs a fail-closed callback before the sync starts.
// It runs while the refresh lock is held so a failed refresh cannot invalidate
// a newer snapshot installed by a concurrent successful refresh.
func (s *Sync) SetFailureHandler(handler func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onRefreshFailure = handler
}

func (s *Sync) Timeout() time.Duration {
	if s == nil {
		return 0
	}
	return s.timeout
}

// Revision returns the last database revision whose complete snapshot was
// successfully installed in this process.
func (s *Sync) Revision() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRevision
}

// Refresh rebuilds the entire snapshot and advances the observed database
// revision under one process-local lock.
func (s *Sync) Refresh(ctx context.Context) error {
	started := time.Now()
	status := "success"
	defer func() {
		if s != nil && s.metrics != nil {
			s.metrics.ObserveInfrastructure("policy_sync", s.component, "refresh", status, started)
		}
	}()
	if s == nil || s.revision == nil || (s.refresh == nil && s.snapshotRefresh == nil) {
		status = "error"
		return errors.New("policy sync is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refreshCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var revision string
	var err error
	if s.snapshotRefresh != nil {
		revision, err = s.snapshotRefresh(refreshCtx)
	} else {
		err = s.refresh(refreshCtx)
		if err == nil {
			revision, err = s.revision(refreshCtx)
		}
	}
	if err != nil {
		status = "error"
		s.invalidateLocked()
		return err
	}
	s.lastRevision = revision
	s.invalidated = false
	return nil
}

func (s *Sync) invalidateLocked() {
	s.invalidated = true
	if s.onRefreshFailure != nil {
		s.onRefreshFailure()
	}
}

// Changed refreshes the local instance synchronously, then publishes a lossy
// invalidation hint. A failed publish is logged; database polling still
// guarantees eventual convergence.
func (s *Sync) Changed(ctx context.Context) error {
	refreshErr := s.Refresh(ctx)
	if s.redis != nil && s.channel != "" {
		started := time.Now()
		if err := s.redis.Publish(ctx, s.channel, "changed").Err(); err != nil {
			if s.metrics != nil {
				s.metrics.ObserveInfrastructure("policy_sync", s.component, "publish", "error", started)
			}
			s.logger.WarnContext(ctx, "publish policy snapshot invalidation failed", "channel", s.channel, "error", observability.SafeError(err, errPolicyInvalidationPublishFailed))
		} else if s.metrics != nil {
			s.metrics.ObserveInfrastructure("policy_sync", s.component, "publish", "success", started)
		}
	}
	return refreshErr
}

// Run watches both Redis notifications and the authoritative database. It
// returns only when ctx is cancelled or the subscription is closed.
func (s *Sync) Run(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	var messages <-chan *redis.Message
	var subscription *redis.PubSub
	if s.redis != nil && s.channel != "" {
		subscription = s.redis.Subscribe(ctx, s.channel)
		messages = subscription.Channel()
		defer func() { _ = subscription.Close() }()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-messages:
			if !ok {
				messages = nil
				continue
			}
			s.refreshAndLog(ctx, "notification")
		case <-ticker.C:
			s.poll(ctx)
		}
	}
}

func (s *Sync) poll(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	revision, err := s.revision(checkCtx)
	if err != nil {
		s.mu.Lock()
		s.invalidateLocked()
		s.mu.Unlock()
		s.logger.ErrorContext(ctx, "read policy snapshot revision failed", "channel", s.channel, "error", observability.SafeError(err, errPolicyRevisionReadFailed))
		return
	}
	s.mu.Lock()
	changed := revision != s.lastRevision || s.invalidated
	s.mu.Unlock()
	if changed {
		s.refreshAndLog(ctx, "revision")
	}
}

func (s *Sync) refreshAndLog(ctx context.Context, source string) {
	if err := s.Refresh(ctx); err != nil {
		s.logger.ErrorContext(ctx, "refresh policy snapshot failed", "channel", s.channel, "source", source, "error", observability.SafeError(err, errPolicySnapshotRefreshFailed))
	}
}
