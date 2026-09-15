package routepolicy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	"github.com/redis/go-redis/v9"
)

type Manager struct {
	repository   *Repository
	snapshot     *Snapshot
	redis        *redis.Client
	channel      string
	pollInterval time.Duration
	logger       *slog.Logger
	metrics      *observability.Metrics
	revision     atomic.Int64
	refresh      sync.Mutex
	routeIDs     sync.Map
}

func NewManager(repository *Repository, compiler *Compiler, client *redis.Client, cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) *Manager {
	return &Manager{
		repository:   repository,
		snapshot:     NewSnapshot(compiler),
		redis:        client,
		channel:      cfg.Runtime.ActiveProfile + ":" + cfg.App.Name + ":route-policy:changed",
		pollInterval: cfg.Authorization.PolicyRefreshInterval,
		logger:       logger,
		metrics:      metrics,
	}
}

func (m *Manager) Refresh(ctx context.Context) error {
	started := time.Now()
	status := "success"
	defer func() {
		m.metrics.ObserveInfrastructure("authorization_policy", "database", "refresh", status, started)
	}()
	m.refresh.Lock()
	defer m.refresh.Unlock()
	definitions, err := m.repository.Load(ctx)
	if err != nil {
		status = "error"
		return err
	}
	if err := m.snapshot.Replace(definitions); err != nil {
		status = "error"
		return err
	}
	revision, err := m.repository.Revision(ctx)
	if err != nil {
		status = "error"
		return err
	}
	m.revision.Store(revision.UnixNano())
	return nil
}

func (m *Manager) ValidateRoutes(ctx context.Context, serviceName string) error {
	ids, err := m.repository.ActiveRouteIDs(ctx, serviceName)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := m.snapshot.Resolve(id); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Evaluate(ctx context.Context, routeID string, authorizer platformauthz.Authorizer) error {
	policy, err := m.snapshot.Resolve(routeID)
	if err != nil {
		return err
	}
	return policy.Evaluate(ctx, authorizer)
}

func (m *Manager) EvaluateRoute(ctx context.Context, protocol, method, path, serviceName string, authorizer platformauthz.Authorizer) error {
	key := protocol + "\x00" + method + "\x00" + path + "\x00" + serviceName
	if cached, ok := m.routeIDs.Load(key); ok {
		routeID, valid := cached.(string)
		if valid {
			return m.Evaluate(ctx, routeID, authorizer)
		}
		m.routeIDs.Delete(key)
	}
	route, err := NewRoute(protocol, method, path, serviceName, "")
	if err != nil {
		return err
	}
	m.routeIDs.Store(key, route.ID)
	return m.Evaluate(ctx, route.ID, authorizer)
}

func (m *Manager) Notify(ctx context.Context) error {
	if m.redis == nil {
		return nil
	}
	started := time.Now()
	if err := m.redis.Publish(ctx, m.channel, "refresh").Err(); err != nil {
		m.metrics.ObserveInfrastructure("authorization_policy", "redis", "publish", "error", started)
		return fmt.Errorf("publish route policy refresh: %w", err)
	}
	m.metrics.ObserveInfrastructure("authorization_policy", "redis", "publish", "success", started)
	return nil
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	if m.redis == nil {
		m.runPolling(ctx, ticker)
		return
	}
	subscription := m.redis.Subscribe(ctx, m.channel)
	defer func() {
		if err := subscription.Close(); err != nil {
			m.logger.Warn("close route policy subscription", "error", err)
		}
	}()
	messages := subscription.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-messages:
			if !ok {
				m.logger.Warn("route policy subscription closed; continuing with database polling")
				m.runPolling(ctx, ticker)
				return
			}
			m.reloadAndLog(ctx, "redis")
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

func (m *Manager) runPolling(ctx context.Context, ticker *time.Ticker) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

func (m *Manager) poll(ctx context.Context) {
	revision, err := m.repository.Revision(ctx)
	if err != nil {
		m.logger.Warn("check route policy revision", "error", err)
		return
	}
	if revision.UnixNano() == m.revision.Load() {
		return
	}
	m.reloadAndLog(ctx, "poll")
}

func (m *Manager) reloadAndLog(ctx context.Context, source string) {
	if err := m.Refresh(ctx); err != nil && !errors.Is(err, context.Canceled) {
		m.logger.Error("refresh route policy cache", "source", source, "error", err)
		return
	}
	m.logger.Info("route policy cache refreshed", "source", source)
}
