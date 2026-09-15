package operationlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const eventType = "platform.operation-log.recorded.v1"

var ErrInvalidEntry = errors.New("invalid operation log entry")

type Entry struct {
	Operation     string        `json:"operation"`
	ResourceType  string        `json:"resource_type,omitempty"`
	ResourceID    string        `json:"resource_id,omitempty"`
	ApplicationID string        `json:"application_id,omitempty"`
	Source        string        `json:"source"`
	Protocol      string        `json:"protocol"`
	Method        string        `json:"method,omitempty"`
	Route         string        `json:"route,omitempty"`
	Request       any           `json:"request,omitempty"`
	Duration      time.Duration `json:"-"`
	Succeeded     bool          `json:"succeeded"`
	ErrorCode     string        `json:"error_code,omitempty"`
	ErrorMessage  string        `json:"error_message,omitempty"`
	ClientIP      string        `json:"client_ip,omitempty"`
	UserAgent     string        `json:"user_agent,omitempty"`
	Extension     any           `json:"-"`
}

type eventPayload struct {
	Entry
	RequestPayload string          `json:"request_payload"`
	DurationMS     int64           `json:"duration_ms"`
	RequestID      string          `json:"request_id"`
	TraceID        string          `json:"trace_id"`
	ActorID        string          `json:"actor_id"`
	ActorType      string          `json:"actor_type"`
	TenantID       string          `json:"tenant_id"`
	OccurredAt     time.Time       `json:"occurred_at"`
	Extension      json.RawMessage `json:"extension"`
}

type Recorder interface {
	Enabled() bool
	Record(ctx context.Context, entry Entry) error
}

// Do measures a business operation and enqueues its result after fn returns.
// The business error is returned unchanged; an enqueue failure is returned only
// when the business operation itself succeeded.
func Do(ctx context.Context, recorder Recorder, entry Entry, fn func() error) error {
	started := time.Now()
	err := fn()
	entry.Duration = time.Since(started)
	entry.Succeeded = err == nil
	if err != nil {
		entry.ErrorMessage = err.Error()
	}
	recordErr := recorder.Record(ctx, entry)
	if err != nil {
		return err
	}
	return recordErr
}

type Service struct {
	enabled    bool
	cfg        config.OperationLog
	appName    string
	bus        *eventbus.Bus
	db         *sqlx.DB
	transactor *database.Transactor
	metrics    *observability.Metrics
}

func New(cfg config.Config, bus *eventbus.Bus, db *sqlx.DB, transactor *database.Transactor, metrics *observability.Metrics) *Service {
	return &Service{enabled: cfg.OperationLog.Enabled, cfg: cfg.OperationLog, appName: cfg.App.Name, bus: bus, db: db, transactor: transactor, metrics: metrics}
}

func (s *Service) Enabled() bool { return s.enabled }

func (s *Service) Record(ctx context.Context, entry Entry) error {
	if !s.enabled {
		return nil
	}
	if strings.TrimSpace(entry.Operation) == "" || entry.Duration < 0 {
		return fmt.Errorf("%w: operation and non-negative duration are required", ErrInvalidEntry)
	}
	principal, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return platformprincipal.ErrMissing
	}
	requestPayload, err := sanitizedJSON(entry.Request, s.cfg.MaxPayloadBytes)
	if err != nil {
		return fmt.Errorf("%w: sanitize operation request: %v", ErrInvalidEntry, err)
	}
	extension, err := sanitizedRawJSON(entry.Extension, s.cfg.MaxPayloadBytes)
	if err != nil {
		return fmt.Errorf("%w: sanitize operation extension: %v", ErrInvalidEntry, err)
	}
	requestID, _ := requestid.FromContext(ctx)
	span := trace.SpanContextFromContext(ctx)
	now := time.Now()
	payload := eventPayload{Entry: entry, RequestPayload: requestPayload, DurationMS: entry.Duration.Milliseconds(), RequestID: requestID, TraceID: span.TraceID().String(), ActorID: principal.ID, ActorType: string(principal.Type), TenantID: principal.TenantID, OccurredAt: now, Extension: extension}
	payload.Request = nil
	payload.Entry.Extension = nil
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode operation log: %w", err)
	}
	envelope := &commonv1.EventEnvelope{EventId: uuid.NewString(), EventType: eventType, AggregateId: entry.ResourceID, AggregateType: "operation_log", TenantId: principal.TenantID, ApplicationId: entry.ApplicationID, SchemaVersion: 1, OccurredAt: timestamppb.New(now), Context: &commonv1.RequestContext{RequestId: requestID, TraceId: span.TraceID().String(), ActorId: principal.ID, ActorType: string(principal.Type), TenantId: principal.TenantID, MembershipId: principal.MembershipID}, Payload: data}
	started := time.Now()
	err = eventbus.Publish(ctx, s.bus, s.cfg.Subject, envelope)
	status := "success"
	if err != nil {
		status = "error"
	}
	s.metrics.ObserveInfrastructure("operation_log", "jetstream", "enqueue", status, started)
	if err != nil {
		return fmt.Errorf("enqueue operation log: %w", err)
	}
	return nil
}

func (s *Service) consume(ctx context.Context, envelope *commonv1.EventEnvelope) error {
	if envelope.EventType != eventType {
		return nil
	}
	var payload eventPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("decode operation log event: %w", err)
	}
	actorID := payload.ActorID
	if actorID == "" {
		actorID = s.appName + ":operation-log-consumer"
	}
	actorCtx := platformprincipal.SystemContext(ctx, actorID)
	started := time.Now()
	err := s.transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error { return s.insert(actorCtx, tx, envelope.EventId, payload) })
	status := "success"
	if err != nil {
		status = "error"
	}
	s.metrics.ObserveInfrastructure("operation_log", "database", "persist", status, started)
	return err
}

func (s *Service) insert(ctx context.Context, tx *sqlx.Tx, id string, value eventPayload) error {
	now := time.Now()
	query := `INSERT INTO operation_logs (id, tenant_id, actor_id, actor_type, application_id, source, operation, resource_type, resource_id, protocol, method, route, request_payload, duration_ms, succeeded, error_code, error_message, request_id, trace_id, client_ip, user_agent, extension, occurred_at, created_at, created_by, updated_at, updated_by, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CAST(? AS JSON), ?, ?, ?, ?, ?, ?)`
	if tx.DriverName() == "mysql" {
		query = "INSERT IGNORE" + strings.TrimPrefix(query, "INSERT")
	} else {
		query = strings.Replace(query, "CAST(? AS JSON)", "CAST(? AS jsonb)", 1)
		query += " ON CONFLICT (id) DO NOTHING"
	}
	query = tx.Rebind(query)
	_, err := tx.ExecContext(ctx, query, id, value.TenantID, value.ActorID, value.ActorType, value.ApplicationID, value.Source, value.Operation, value.ResourceType, value.ResourceID, value.Protocol, value.Method, value.Route, value.RequestPayload, value.DurationMS, value.Succeeded, value.ErrorCode, truncate(value.ErrorMessage, 2048), value.RequestID, value.TraceID, value.ClientIP, truncate(value.UserAgent, 1024), string(value.Extension), value.OccurredAt, now, value.ActorID, now, value.ActorID, 1)
	if err != nil {
		return fmt.Errorf("insert operation log: %w", err)
	}
	return nil
}

func sanitizedJSON(value any, limit int) (string, error) {
	if value == nil {
		return "", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", err
	}
	redact(decoded)
	data, err = json.Marshal(decoded)
	if err != nil {
		return "", err
	}
	return truncate(string(data), limit), nil
}

func sanitizedRawJSON(value any, limit int) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage("{}"), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	redact(decoded)
	data, err = json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("JSON payload exceeds %d bytes", limit)
	}
	return json.RawMessage(data), nil
}

func redact(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") {
				current[key] = "[REDACTED]"
				continue
			}
			redact(child)
		}
	case []any:
		for _, child := range current {
			redact(child)
		}
	}
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

var _ Recorder = (*Service)(nil)
