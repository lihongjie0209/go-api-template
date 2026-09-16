package securitylog

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	platformredact "github.com/lihongjie0209/microservice-platform-go/redact"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type EventType string

const (
	EventLogin                 EventType = "login"
	EventTokenRefresh          EventType = "token_refresh"
	EventLogout                EventType = "logout"
	EventForcedLogout          EventType = "forced_logout"
	EventPasswordChanged       EventType = "password_changed"
	EventPasswordReset         EventType = "password_reset"
	EventSessionRevoked        EventType = "session_revoked"
	EventLogoutAll             EventType = "logout_all"
	EventMembershipAdded       EventType = "membership_added"
	EventMembershipChanged     EventType = "membership_changed"
	EventMembershipRemoved     EventType = "membership_removed"
	EventRoutePolicyChanged    EventType = "route_policy_changed"
	EventPermissionChanged     EventType = "permission_definition_changed"
	EventIdentityUserChanged   EventType = "identity_user_changed"
	EventTenantChanged         EventType = "tenant_changed"
	EventTenantContextSwitch   EventType = "tenant_context_switch"
	EventTenantAuthorization   EventType = "tenant_authorization_changed"
	EventMenuChanged           EventType = "platform_menu_changed"
	EventPlatformConfigChanged EventType = "platform_config_changed"
	EventSecurityLogAccess     EventType = "security_log_accessed"
	EventServiceAccountChanged EventType = "service_account_changed"
)

const envelopeType = "platform.security-log.recorded.v1"

var ErrInvalidEntry = errors.New("invalid security log entry")

type Entry struct {
	EventType    EventType `json:"event_type"`
	SubjectID    string    `json:"subject_id,omitempty"`
	SubjectType  string    `json:"subject_type,omitempty"`
	TenantID     string    `json:"tenant_id,omitempty"`
	Identifier   string    `json:"-"`
	TokenID      string    `json:"-"`
	SessionID    string    `json:"session_id,omitempty"`
	Succeeded    bool      `json:"succeeded"`
	Reason       string    `json:"reason,omitempty"`
	ErrorCode    string    `json:"error_code,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	ClientIP     string    `json:"client_ip,omitempty"`
	UserAgent    string    `json:"user_agent,omitempty"`
	Metadata     any       `json:"-"`
}

type payload struct {
	EventType      EventType       `json:"event_type"`
	ActorID        string          `json:"actor_id"`
	ActorType      string          `json:"actor_type"`
	SubjectID      string          `json:"subject_id"`
	SubjectType    string          `json:"subject_type"`
	TenantID       string          `json:"tenant_id"`
	Succeeded      bool            `json:"succeeded"`
	Reason         string          `json:"reason"`
	ErrorCode      string          `json:"error_code"`
	ErrorMessage   string          `json:"error_message"`
	IdentifierHash string          `json:"identifier_hash"`
	TokenIDHash    string          `json:"token_id_hash"`
	SessionID      string          `json:"session_id"`
	RequestID      string          `json:"request_id"`
	TraceID        string          `json:"trace_id"`
	ClientIP       string          `json:"client_ip"`
	UserAgent      string          `json:"user_agent"`
	Metadata       json.RawMessage `json:"metadata"`
	OccurredAt     time.Time       `json:"occurred_at"`
}

type Recorder interface {
	Enabled() bool
	FailClosed() bool
	Record(ctx context.Context, entry Entry) error
}

type TransactionalRecorder interface {
	Enabled() bool
	FailClosed() bool
	RecordTx(ctx context.Context, tx *sqlx.Tx, entry Entry) error
}

type Service struct {
	cfg        config.SecurityLog
	appName    string
	bus        *eventbus.Bus
	db         *sqlx.DB
	transactor *database.Transactor
	metrics    *observability.Metrics
	outbox     *eventbus.Outbox
}

func New(cfg config.Config, bus *eventbus.Bus, db *sqlx.DB, transactor *database.Transactor, metrics *observability.Metrics, outbox *eventbus.Outbox) *Service {
	return &Service{cfg: cfg.SecurityLog, appName: cfg.App.Name, bus: bus, db: db, transactor: transactor, metrics: metrics, outbox: outbox}
}
func (s *Service) Enabled() bool    { return s.cfg.Enabled }
func (s *Service) FailClosed() bool { return s.cfg.FailClosed }

func (s *Service) Record(ctx context.Context, entry Entry) error {
	if !s.Enabled() {
		return nil
	}
	envelope, err := s.envelope(ctx, entry)
	if err != nil {
		return err
	}
	started := time.Now()
	err = eventbus.Publish(ctx, s.bus, s.cfg.Subject, envelope)
	status := "success"
	if err != nil {
		status = "error"
	}
	s.metrics.ObserveInfrastructure("security_log", "jetstream", "enqueue", status, started)
	if err != nil {
		return fmt.Errorf("enqueue security log: %w", err)
	}
	return nil
}

// RecordTx stores a security event in the transactional outbox owned by tx.
// A successful security-critical mutation must use this method so the domain
// change and its durable audit event commit or roll back together.
func (s *Service) RecordTx(ctx context.Context, tx *sqlx.Tx, entry Entry) error {
	if !s.Enabled() {
		return nil
	}
	envelope, err := s.envelope(ctx, entry)
	if err != nil {
		return err
	}
	if err := s.outbox.Store(ctx, tx, s.cfg.Subject, envelope); err != nil {
		return fmt.Errorf("store security log in outbox: %w", err)
	}
	return nil
}

func (s *Service) envelope(ctx context.Context, entry Entry) (*commonv1.EventEnvelope, error) {
	if !validEvent(entry.EventType) {
		return nil, fmt.Errorf("%w: unsupported event type %q", ErrInvalidEntry, entry.EventType)
	}
	actor, _ := platformprincipal.FromContext(ctx)
	clientIP, userAgent := clientFromContext(ctx)
	if entry.ClientIP == "" {
		entry.ClientIP = clientIP
	}
	if entry.UserAgent == "" {
		entry.UserAgent = userAgent
	}
	if entry.TenantID == "" {
		entry.TenantID = actor.TenantID
	}
	metadata, err := safeMetadata(entry.Metadata, s.cfg.MaxPayloadBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidEntry, err)
	}
	requestID, _ := requestid.FromContext(ctx)
	span := trace.SpanContextFromContext(ctx)
	now := time.Now()
	value := payload{EventType: entry.EventType, ActorID: actor.ID, ActorType: string(actor.Type), SubjectID: entry.SubjectID, SubjectType: entry.SubjectType, TenantID: entry.TenantID, Succeeded: entry.Succeeded, Reason: truncate(entry.Reason, 1024), ErrorCode: truncate(entry.ErrorCode, 128), ErrorMessage: truncate(entry.ErrorMessage, 2048), IdentifierHash: s.hashIdentifier(entry.Identifier), TokenIDHash: s.hash(entry.TokenID), SessionID: truncate(entry.SessionID, 256), RequestID: requestID, TraceID: span.TraceID().String(), ClientIP: truncate(entry.ClientIP, 256), UserAgent: truncate(entry.UserAgent, 1024), Metadata: metadata, OccurredAt: now}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode security log: %w", err)
	}
	envelope := &commonv1.EventEnvelope{EventId: uuid.NewString(), EventType: envelopeType, AggregateId: entry.SubjectID, AggregateType: "security_log", TenantId: entry.TenantID, SchemaVersion: 1, OccurredAt: timestamppb.New(now), Context: &commonv1.RequestContext{RequestId: requestID, TraceId: span.TraceID().String(), ActorId: actor.ID, ActorType: string(actor.Type), TenantId: entry.TenantID}, Payload: data}
	return envelope, nil
}

func (s *Service) consume(ctx context.Context, envelope *commonv1.EventEnvelope) error {
	if envelope.EventType != envelopeType {
		return nil
	}
	var value payload
	if err := json.Unmarshal(envelope.Payload, &value); err != nil {
		return fmt.Errorf("decode security log: %w", err)
	}
	auditActor := value.ActorID
	if auditActor == "" {
		auditActor = s.appName + ":security-log-consumer"
	}
	actorCtx := platformprincipal.SystemContext(ctx, auditActor)
	started := time.Now()
	err := s.transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error { return insert(actorCtx, tx, envelope.EventId, value, auditActor) })
	status := "success"
	if err != nil {
		status = "error"
	}
	s.metrics.ObserveInfrastructure("security_log", "database", "persist", status, started)
	return err
}

func insert(ctx context.Context, tx *sqlx.Tx, id string, value payload, auditActor string) error {
	now := time.Now()
	query := `INSERT INTO security_logs (id, tenant_id, actor_id, actor_type, subject_id, subject_type, event_type, succeeded, reason, error_code, error_message, identifier_hash, token_id_hash, session_id, request_id, trace_id, client_ip, user_agent, metadata, occurred_at, created_at, created_by, updated_at, updated_by, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CAST(? AS JSON), ?, ?, ?, ?, ?, ?)`
	if tx.DriverName() == "mysql" {
		query = "INSERT IGNORE" + strings.TrimPrefix(query, "INSERT")
	} else {
		query = strings.Replace(query, "CAST(? AS JSON)", "CAST(? AS jsonb)", 1) + " ON CONFLICT (id, occurred_at) DO NOTHING"
	}
	query = tx.Rebind(query)
	_, err := tx.ExecContext(ctx, query, id, value.TenantID, value.ActorID, value.ActorType, value.SubjectID, value.SubjectType, value.EventType, value.Succeeded, value.Reason, value.ErrorCode, value.ErrorMessage, value.IdentifierHash, value.TokenIDHash, value.SessionID, value.RequestID, value.TraceID, value.ClientIP, value.UserAgent, string(value.Metadata), value.OccurredAt, now, auditActor, now, auditActor, 1)
	if err != nil {
		return fmt.Errorf("insert security log: %w", err)
	}
	return nil
}

func (s *Service) hash(value string) string {
	if value == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.HashKey))
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) hashIdentifier(value string) string {
	return s.hash(strings.ToLower(strings.TrimSpace(value)))
}
func validEvent(value EventType) bool {
	return value == EventLogin || value == EventTokenRefresh || value == EventLogout || value == EventForcedLogout || value == EventPasswordChanged || value == EventPasswordReset || value == EventSessionRevoked || value == EventLogoutAll || value == EventMembershipAdded || value == EventMembershipChanged || value == EventMembershipRemoved || value == EventRoutePolicyChanged || value == EventPermissionChanged || value == EventIdentityUserChanged || value == EventTenantChanged || value == EventTenantContextSwitch || value == EventTenantAuthorization || value == EventMenuChanged || value == EventPlatformConfigChanged || value == EventSecurityLogAccess || value == EventServiceAccountChanged
}
func safeMetadata(value any, limit int) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage("{}"), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("metadata exceeds %d bytes", limit)
	}
	sensitive, err := platformredact.ContainsSensitiveJSON(data)
	if err != nil {
		return nil, err
	}
	if sensitive {
		return nil, errors.New("metadata contains a credential field")
	}
	return data, nil
}
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

var _ Recorder = (*Service)(nil)
var _ TransactionalRecorder = (*Service)(nil)
