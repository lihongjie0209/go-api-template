package operationlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	platformredact "github.com/lihongjie0209/microservice-platform-go/redact"
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

type TransactionalRecorder interface {
	Recorder
	RecordTx(ctx context.Context, tx *sqlx.Tx, entry Entry) error
}

type outboxStore interface {
	Store(context.Context, *sqlx.Tx, string, *commonv1.EventEnvelope) error
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
	outbox     outboxStore
	db         *sqlx.DB
	transactor *database.Transactor
	metrics    *observability.Metrics
}

func New(cfg config.Config, bus *eventbus.Bus, db *sqlx.DB, transactor *database.Transactor, metrics *observability.Metrics, outbox *eventbus.Outbox) *Service {
	return &Service{enabled: cfg.OperationLog.Enabled, cfg: cfg.OperationLog, appName: cfg.App.Name, bus: bus, outbox: outbox, db: db, transactor: transactor, metrics: metrics}
}

func (s *Service) Enabled() bool { return s.enabled }

func (s *Service) Record(ctx context.Context, entry Entry) error {
	if !s.enabled {
		return nil
	}
	if s.transactor == nil || s.outbox == nil {
		return errors.New("operation log outbox is unavailable")
	}
	envelope, err := s.envelope(ctx, entry)
	if err != nil {
		return err
	}
	started := time.Now()
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	err = s.transactor.Within(persistCtx, nil, func(tx *sqlx.Tx) error {
		return s.outbox.Store(persistCtx, tx, s.cfg.Subject, envelope)
	})
	s.observeEnqueue(started, err)
	if err != nil {
		return fmt.Errorf("store operation log in outbox: %w", err)
	}
	return nil
}

// RecordTx atomically stores the operation event with a caller-owned domain
// transaction. Successful mutations should prefer this boundary; a rollback
// removes both the domain write and its pending event.
func (s *Service) RecordTx(ctx context.Context, tx *sqlx.Tx, entry Entry) error {
	if !s.enabled {
		return nil
	}
	if tx == nil || s.outbox == nil {
		return errors.New("operation log transactional outbox is unavailable")
	}
	envelope, err := s.envelope(ctx, entry)
	if err != nil {
		return err
	}
	started := time.Now()
	err = s.outbox.Store(ctx, tx, s.cfg.Subject, envelope)
	s.observeEnqueue(started, err)
	if err != nil {
		return fmt.Errorf("store operation log in outbox: %w", err)
	}
	return nil
}

func (s *Service) envelope(ctx context.Context, entry Entry) (*commonv1.EventEnvelope, error) {
	if err := validateEntry(entry); err != nil {
		return nil, err
	}
	principal, ok := platformprincipal.FromContext(ctx)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return nil, platformprincipal.ErrMissing
	}
	requestPayload, err := sanitizedJSON(entry.Request, s.cfg.MaxPayloadBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: sanitize operation request: %v", ErrInvalidEntry, err)
	}
	extension, err := sanitizedRawJSON(entry.Extension, s.cfg.MaxPayloadBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: sanitize operation extension: %v", ErrInvalidEntry, err)
	}
	requestID, _ := requestid.FromContext(ctx)
	span := trace.SpanContextFromContext(ctx)
	now := time.Now()
	payload := eventPayload{Entry: entry, RequestPayload: requestPayload, DurationMS: entry.Duration.Milliseconds(), RequestID: requestID, TraceID: span.TraceID().String(), ActorID: principal.ID, ActorType: string(principal.Type), TenantID: principal.TenantID, OccurredAt: now, Extension: extension}
	payload.Request = nil
	payload.Entry.Extension = nil
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode operation log: %w", err)
	}
	return &commonv1.EventEnvelope{EventId: uuid.NewString(), EventType: eventType, AggregateId: entry.ResourceID, AggregateType: "operation_log", TenantId: principal.TenantID, ApplicationId: entry.ApplicationID, SchemaVersion: 1, OccurredAt: timestamppb.New(now), Context: &commonv1.RequestContext{RequestId: requestID, TraceId: span.TraceID().String(), ActorId: principal.ID, ActorType: string(principal.Type), TenantId: principal.TenantID, MembershipId: principal.MembershipID}, Payload: data}, nil
}

func (s *Service) observeEnqueue(started time.Time, err error) {
	status := "success"
	if err != nil {
		status = "error"
	}
	if s.metrics != nil {
		s.metrics.ObserveInfrastructure("operation_log", "database", "enqueue", status, started)
	}
}

func (s *Service) consume(ctx context.Context, envelope *commonv1.EventEnvelope) error {
	if envelope.EventType != eventType {
		return nil
	}
	var payload eventPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("decode operation log event: %w", err)
	}
	if err := s.validateConsumedEvent(envelope, payload); err != nil {
		return err
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
	if s.metrics != nil {
		s.metrics.ObserveInfrastructure("operation_log", "database", "persist", status, started)
	}
	return err
}

func (s *Service) validateConsumedEvent(envelope *commonv1.EventEnvelope, payload eventPayload) error {
	if envelope == nil || envelope.EventId == "" || len(envelope.EventId) > 256 || envelope.EventType != eventType || envelope.SchemaVersion != 1 || envelope.OccurredAt == nil || envelope.Context == nil {
		return fmt.Errorf("%w: invalid operation log envelope", ErrInvalidEntry)
	}
	if err := envelope.OccurredAt.CheckValid(); err != nil || !envelope.OccurredAt.AsTime().Equal(payload.OccurredAt) {
		return fmt.Errorf("%w: inconsistent operation log occurrence time", ErrInvalidEntry)
	}
	if envelope.TenantId != payload.TenantID || envelope.ApplicationId != payload.ApplicationID ||
		envelope.Context.ActorId != payload.ActorID || envelope.Context.ActorType != payload.ActorType ||
		envelope.Context.TenantId != payload.TenantID || envelope.Context.RequestId != payload.RequestID ||
		envelope.Context.TraceId != payload.TraceID {
		return fmt.Errorf("%w: inconsistent operation log envelope context", ErrInvalidEntry)
	}
	if payload.DurationMS < 0 || payload.DurationMS > int64((24*time.Hour)/time.Millisecond) {
		return fmt.Errorf("%w: invalid operation log duration", ErrInvalidEntry)
	}
	entry := payload.Entry
	entry.Duration = time.Duration(payload.DurationMS) * time.Millisecond
	if err := validateEntry(entry); err != nil || payload.ActorID == "" || len(payload.ActorID) > 256 || len(payload.ActorType) > 64 || len(payload.TenantID) > 256 || len(payload.RequestID) > 256 || len(payload.TraceID) > 64 || len(payload.RequestPayload) > s.cfg.MaxPayloadBytes || len(payload.Extension) > s.cfg.MaxPayloadBytes || !json.Valid(payload.Extension) {
		return fmt.Errorf("%w: invalid operation log event payload", ErrInvalidEntry)
	}
	var extension map[string]any
	if err := json.Unmarshal(payload.Extension, &extension); err != nil || extension == nil {
		return fmt.Errorf("%w: extension must be a JSON object", ErrInvalidEntry)
	}
	if payload.RequestPayload != "" && !json.Valid([]byte(payload.RequestPayload)) {
		return fmt.Errorf("%w: request payload must be valid JSON", ErrInvalidEntry)
	}
	return nil
}

func (s *Service) insert(ctx context.Context, tx *sqlx.Tx, id string, value eventPayload) error {
	now := time.Now()
	query := `INSERT INTO operation_logs (id, tenant_id, actor_id, actor_type, application_id, source, operation, resource_type, resource_id, protocol, method, route, request_payload, duration_ms, succeeded, error_code, error_message, request_id, trace_id, client_ip, user_agent, extension, occurred_at, created_at, created_by, updated_at, updated_by, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CAST(? AS JSON), ?, ?, ?, ?, ?, ?)`
	if tx.DriverName() == "mysql" {
		query = "INSERT IGNORE" + strings.TrimPrefix(query, "INSERT")
	} else {
		query = strings.Replace(query, "CAST(? AS JSON)", "CAST(? AS jsonb)", 1)
		query += " ON CONFLICT (id, occurred_at) DO NOTHING"
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
	data, err = platformredact.JSON(data)
	if err != nil {
		return "", err
	}
	if len(data) <= limit {
		return string(data), nil
	}
	marker, err := json.Marshal(map[string]any{"original_bytes": len(data), "truncated": true})
	if err != nil {
		return "", err
	}
	if len(marker) > limit {
		return "", fmt.Errorf("payload limit %d is too small for truncation marker", limit)
	}
	return string(marker), nil
}

func sanitizedRawJSON(value any, limit int) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage("{}"), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	data, err = platformredact.JSON(data)
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("JSON payload exceeds %d bytes", limit)
	}
	return json.RawMessage(data), nil
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func validateEntry(entry Entry) error {
	if strings.TrimSpace(entry.Operation) == "" || entry.Duration < 0 || entry.Duration > 24*time.Hour {
		return fmt.Errorf("%w: operation and duration from zero through 24h are required", ErrInvalidEntry)
	}
	for name, value := range map[string]string{
		"operation": entry.Operation, "resource_type": entry.ResourceType, "resource_id": entry.ResourceID,
		"application_id": entry.ApplicationID, "source": entry.Source, "protocol": entry.Protocol,
		"method": entry.Method, "route": entry.Route, "error_code": entry.ErrorCode,
		"error_message": entry.ErrorMessage, "client_ip": entry.ClientIP, "user_agent": entry.UserAgent,
	} {
		if !utf8.ValidString(value) {
			return fmt.Errorf("%w: %s must be valid UTF-8", ErrInvalidEntry, name)
		}
	}
	if strings.TrimSpace(entry.Source) == "" || strings.TrimSpace(entry.Protocol) == "" ||
		len(entry.Operation) > 256 || len(entry.ResourceType) > 128 || len(entry.ResourceID) > 256 ||
		len(entry.ApplicationID) > 128 || len(entry.Source) > 32 || len(entry.Protocol) > 32 ||
		len(entry.Method) > 32 || len(entry.Route) > 1024 || len(entry.ErrorCode) > 128 ||
		len(entry.ErrorMessage) > 2048 || len(entry.ClientIP) > 128 || len(entry.UserAgent) > 4096 {
		return fmt.Errorf("%w: operation log fields exceed their bounds", ErrInvalidEntry)
	}
	return nil
}

var _ Recorder = (*Service)(nil)
var _ TransactionalRecorder = (*Service)(nil)
