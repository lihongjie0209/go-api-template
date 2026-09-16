package policysync

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultPollInterval = 30 * time.Second

// Sync keeps an immutable in-process policy snapshot aligned with its
// authoritative database state. Pub/Sub reduces propagation latency; revision
// polling is the correctness fallback because Redis notifications are lossy.
type Sync struct {
	redis        redis.UniversalClient
	channel      string
	pollInterval time.Duration
	timeout      time.Duration
	revision     func(context.Context) (string, error)
	refresh      func(context.Context) error
	logger       *slog.Logger
	component    string
	metrics      metricsObserver

	mu           sync.Mutex
	lastRevision string
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
	if s == nil || s.revision == nil || s.refresh == nil {
		status = "error"
		return errors.New("policy sync is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refreshCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.refresh(refreshCtx); err != nil {
		status = "error"
		return err
	}
	revision, err := s.revision(refreshCtx)
	if err != nil {
		status = "error"
		return err
	}
	s.lastRevision = revision
	return nil
}

// Changed refreshes the local instance synchronously, then publishes a lossy
// invalidation hint. A failed publish is logged; database polling still
// guarantees eventual convergence.
func (s *Sync) Changed(ctx context.Context) error {
	if err := s.Refresh(ctx); err != nil {
		return err
	}
	if s.redis != nil && s.channel != "" {
		started := time.Now()
		if err := s.redis.Publish(ctx, s.channel, "changed").Err(); err != nil {
			if s.metrics != nil {
				s.metrics.ObserveInfrastructure("policy_sync", s.component, "publish", "error", started)
			}
			s.logger.WarnContext(ctx, "publish policy snapshot invalidation failed", "channel", s.channel, "error", err)
		} else if s.metrics != nil {
			s.metrics.ObserveInfrastructure("policy_sync", s.component, "publish", "success", started)
		}
	}
	return nil
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
		s.logger.ErrorContext(ctx, "read policy snapshot revision failed", "channel", s.channel, "error", err)
		return
	}
	s.mu.Lock()
	changed := revision != s.lastRevision
	s.mu.Unlock()
	if changed {
		s.refreshAndLog(ctx, "revision")
	}
}

func (s *Sync) refreshAndLog(ctx context.Context, source string) {
	if err := s.Refresh(ctx); err != nil {
		s.logger.ErrorContext(ctx, "refresh policy snapshot failed", "channel", s.channel, "source", source, "error", err)
	}
}
