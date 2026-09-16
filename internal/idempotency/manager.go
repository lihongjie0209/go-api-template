package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
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
	next             *platformidempotency.Manager
	metrics          *observability.Metrics
	maxResponseBytes int
	fingerprintScope string
}

const defaultMaxResponseBytes = 1 << 20

const (
	StateAcquired   = platformidempotency.StateAcquired
	StateProcessing = platformidempotency.StateProcessing
	StateCompleted  = platformidempotency.StateCompleted
	StateFailed     = platformidempotency.StateFailed
	StateConflict   = platformidempotency.StateConflict
)

func New(client *redis.Client, cfg config.Config, metrics *observability.Metrics) *Manager {
	// The SDK prepends its own "idempotency:" segment. Reuse the mandatory
	// environment/service Redis namespace, without its delimiter, so a shared
	// Redis deployment cannot replay results across environments.
	serviceNamespace := strings.TrimSuffix(cfg.RedisKeyPrefix(), ":")
	maxResponseBytes := cfg.Idempotency.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	profile := strings.ToLower(strings.TrimSpace(cfg.Runtime.ActiveProfile))
	if profile == "" {
		profile = strings.ToLower(strings.TrimSpace(cfg.App.Env))
	}
	if profile == "" {
		profile = "development"
	}
	return &Manager{next: platformidempotency.New(client, platformidempotency.Config{
		Enabled:          cfg.Idempotency.Enabled,
		Service:          serviceNamespace,
		ProcessingTTL:    cfg.Idempotency.ProcessingTTL,
		ResultTTL:        cfg.Idempotency.ResultTTL,
		FailureTTL:       cfg.Idempotency.FailureTTL,
		MaxResponseBytes: cfg.Idempotency.MaxResponseBytes,
	}), metrics: metrics, maxResponseBytes: maxResponseBytes, fingerprintScope: profile + "\x00" + strings.TrimSpace(cfg.App.Name)}
}

func (m *Manager) Enabled() bool { return m != nil && m.next.Enabled() }

func (m *Manager) MaxResponseBytes() int {
	if m == nil || m.maxResponseBytes <= 0 {
		return defaultMaxResponseBytes
	}
	return m.maxResponseBytes
}

func (m *Manager) Begin(ctx context.Context, key, fingerprint string) (Decision, error) {
	started := time.Now()
	ctx, span := tracer.Start(ctx, "idempotency.begin")
	decision, err := m.next.Begin(ctx, key, m.scopedFingerprint(fingerprint))
	m.observe("begin", decisionStatus(decision, err), started)
	finishSpan(span, decisionStatus(decision, err), err)
	return decision, err
}

func (m *Manager) scopedFingerprint(fingerprint string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(m.fingerprintScope))
	_, _ = hash.Write([]byte("\x00"))
	_, _ = hash.Write([]byte(fingerprint))
	return hex.EncodeToString(hash.Sum(nil))
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
