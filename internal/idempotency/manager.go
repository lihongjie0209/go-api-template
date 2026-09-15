package idempotency

import (
	"context"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	platformidempotency "github.com/lihongjie0209/microservice-platform-go/idempotency"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("go-api-template/infrastructure/idempotency")

type State = platformidempotency.State
type Failure = platformidempotency.Failure
type Decision = platformidempotency.Decision
type Manager struct {
	next    *platformidempotency.Manager
	metrics *observability.Metrics
}

const (
	StateAcquired   = platformidempotency.StateAcquired
	StateProcessing = platformidempotency.StateProcessing
	StateCompleted  = platformidempotency.StateCompleted
	StateFailed     = platformidempotency.StateFailed
	StateConflict   = platformidempotency.StateConflict
)

func New(client *redis.Client, cfg config.Config, metrics *observability.Metrics) *Manager {
	return &Manager{next: platformidempotency.New(client, platformidempotency.Config{
		Enabled:          cfg.Idempotency.Enabled,
		Service:          cfg.App.Name,
		ProcessingTTL:    cfg.Idempotency.ProcessingTTL,
		ResultTTL:        cfg.Idempotency.ResultTTL,
		FailureTTL:       cfg.Idempotency.FailureTTL,
		MaxResponseBytes: cfg.Idempotency.MaxResponseBytes,
	}), metrics: metrics}
}

func (m *Manager) Enabled() bool { return m != nil && m.next.Enabled() }

func (m *Manager) Begin(ctx context.Context, key, fingerprint string) (Decision, error) {
	started := time.Now()
	ctx, span := tracer.Start(ctx, "idempotency.begin")
	decision, err := m.next.Begin(ctx, key, fingerprint)
	m.observe("begin", decisionStatus(decision, err), started)
	finishSpan(span, decisionStatus(decision, err), err)
	return decision, err
}

func (m *Manager) Complete(ctx context.Context, key, owner string, response any) error {
	started := time.Now()
	ctx, span := tracer.Start(ctx, "idempotency.complete")
	err := m.next.Complete(ctx, key, owner, response)
	m.observe("complete", errorStatus(err), started)
	finishSpan(span, errorStatus(err), err)
	return err
}

func (m *Manager) Fail(ctx context.Context, key, owner string, failure Failure) error {
	started := time.Now()
	ctx, span := tracer.Start(ctx, "idempotency.fail")
	err := m.next.Fail(ctx, key, owner, failure)
	m.observe("fail", errorStatus(err), started)
	finishSpan(span, errorStatus(err), err)
	return err
}

func (m *Manager) Abort(ctx context.Context, key, owner string) error {
	started := time.Now()
	ctx, span := tracer.Start(ctx, "idempotency.abort")
	err := m.next.Abort(ctx, key, owner)
	m.observe("abort", errorStatus(err), started)
	finishSpan(span, errorStatus(err), err)
	return err
}

func (m *Manager) StartLease(ctx context.Context, key, owner string) (context.Context, func() error, error) {
	started := time.Now()
	ctx, span := tracer.Start(ctx, "idempotency.lease_start")
	leaseCtx, stop, err := m.next.StartLease(ctx, key, owner)
	m.observe("lease_start", errorStatus(err), started)
	finishSpan(span, errorStatus(err), err)
	if err != nil {
		return nil, nil, err
	}
	return leaseCtx, func() error {
		stopped := time.Now()
		_, stopSpan := tracer.Start(context.WithoutCancel(ctx), "idempotency.lease_stop")
		stopErr := stop()
		m.observe("lease_stop", errorStatus(stopErr), stopped)
		finishSpan(stopSpan, errorStatus(stopErr), stopErr)
		return stopErr
	}, nil
}

func finishSpan(span trace.Span, status string, err error) {
	span.SetAttributes(attribute.String("idempotency.status", status))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "operation failed")
	}
	span.End()
}

func (m *Manager) observe(operation, status string, started time.Time) {
	if m != nil && m.metrics != nil {
		m.metrics.ObserveInfrastructure("idempotency", "redis", operation, status, started)
	}
}

func decisionStatus(decision Decision, err error) string {
	if err != nil {
		return "error"
	}
	return string(decision.State)
}

func errorStatus(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}
