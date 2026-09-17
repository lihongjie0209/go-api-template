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
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	platformredact "github.com/lihongjie0209/microservice-platform-go/redact"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type EventType string

const (
	EventLogin                    EventType = "login"
	EventTokenRefresh             EventType = "token_refresh"
	EventLogout                   EventType = "logout"
	EventForcedLogout             EventType = "forced_logout"
	EventPasswordChanged          EventType = "password_changed"
	EventPasswordReset            EventType = "password_reset"
	EventSessionRevoked           EventType = "session_revoked"
	EventLogoutAll                EventType = "logout_all"
	EventMembershipAdded          EventType = "membership_added"
	EventMembershipChanged        EventType = "membership_changed"
	EventMembershipRemoved        EventType = "membership_removed"
	EventPBACPolicyChanged        EventType = "pbac_policy_changed"
	EventDataPolicyChanged        EventType = "data_permission_policy_changed"
	EventPermissionChanged        EventType = "permission_definition_changed"
	EventIdentityUserChanged      EventType = "identity_user_changed"
	EventTenantChanged            EventType = "tenant_changed"
	EventTenantContextSwitch      EventType = "tenant_context_switch"
	EventApplicationContextSwitch EventType = "application_context_switch"
	EventTenantAuthorization      EventType = "tenant_authorization_changed"
	EventMenuChanged              EventType = "platform_menu_changed"
	EventPlatformConfigChanged    EventType = "platform_config_changed"
	EventSecurityLogAccess        EventType = "security_log_accessed"
	EventServiceAccountChanged    EventType = "service_account_changed"
)

const envelopeType = "platform.security-log.recorded.v1"

var ErrInvalidEntry = errors.New("invalid security log entry")

type Entry struct {
	EventType    EventType `json:"event_type"`
	SubjectID    string    `json:"subject_id,omitempty"`
	SubjectName  string    `json:"subject_name,omitempty"`
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
	SubjectName    string          `json:"subject_name"`
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
	Recorder
	RecordTx(ctx context.Context, tx *sqlx.Tx, entry Entry) error
}

type outboxStore interface {
	Store(context.Context, *sqlx.Tx, string, *commonv1.EventEnvelope) error
}

type Service struct {
	cfg        config.SecurityLog
	appName    string
	bus        *eventbus.Bus
	db         *sqlx.DB
	transactor *database.Transactor
	metrics    *observability.Metrics
	outbox     outboxStore
	actors     presentation.ActorResolver
}

func New(cfg config.Config, bus *eventbus.Bus, db *sqlx.DB, transactor *database.Transactor, metrics *observability.Metrics, outbox *eventbus.Outbox, actors presentation.ActorResolver) *Service {
	return &Service{cfg: cfg.SecurityLog, appName: cfg.App.Name, bus: bus, db: db, transactor: transactor, metrics: metrics, outbox: outbox, actors: actors}
}
func (s *Service) Enabled() bool    { return s.cfg.Enabled }
func (s *Service) FailClosed() bool { return s.cfg.FailClosed }

func (s *Service) Record(ctx context.Context, entry Entry) error {
	if !s.Enabled() {
		return nil
	}
	if s.transactor == nil || s.outbox == nil {
		return errors.New("security log outbox is unavailable")
	}
	envelope, err := s.envelope(ctx, entry)
	if err != nil {
		return err
	}
	started := time.Now()
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if actor, ok := platformprincipal.FromContext(persistCtx); !ok || strings.TrimSpace(actor.ID) == "" {
		persistCtx = platformprincipal.SystemContext(persistCtx, s.appName+":security-log-recorder")
	}
	err = s.transactor.Within(persistCtx, nil, func(tx *sqlx.Tx) error {
		return s.outbox.Store(persistCtx, tx, s.cfg.Subject, envelope)
	})
	status := "success"
	if err != nil {
		status = "error"
	}
	if s.metrics != nil {
		s.metrics.ObserveInfrastructure("security_log", "database", "enqueue", status, started)
	}
	if err != nil {
		return fmt.Errorf("store security log in outbox: %w", err)
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
	if tx == nil || s.outbox == nil {
		return errors.New("security log transactional outbox is unavailable")
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
	if err := validateEntry(entry); err != nil {
		return nil, err
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
	value := payload{EventType: entry.EventType, ActorID: actor.ID, ActorType: string(actor.Type), SubjectID: entry.SubjectID, SubjectName: entry.SubjectName, SubjectType: entry.SubjectType, TenantID: entry.TenantID, Succeeded: entry.Succeeded, Reason: truncate(entry.Reason, 1024), ErrorCode: truncate(entry.ErrorCode, 128), ErrorMessage: truncate(entry.ErrorMessage, 2048), IdentifierHash: s.hashIdentifier(entry.Identifier), TokenIDHash: s.hash(entry.TokenID), SessionID: truncate(entry.SessionID, 256), RequestID: requestID, TraceID: span.TraceID().String(), ClientIP: truncate(entry.ClientIP, 256), UserAgent: truncate(entry.UserAgent, 1024), Metadata: metadata, OccurredAt: now}
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
	if err := s.validateConsumedEvent(envelope, value); err != nil {
		return err
	}
	auditActor := value.ActorID
	if auditActor == "" {
		auditActor = s.appName + ":security-log-consumer"
	}
	actorCtx := platformprincipal.SystemContext(ctx, auditActor)
	names, err := presentation.ActorNameSnapshots(ctx, s.actors, value.ActorID, value.SubjectID)
	if err != nil {
		return fmt.Errorf("resolve security log name snapshots: %w", err)
	}
	actorName := names[value.ActorID]
	subjectName := value.SubjectName
	if subjectName == "" {
		subjectName = names[value.SubjectID]
	}
	started := time.Now()
	err = s.transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		return insert(actorCtx, tx, envelope.EventId, value, auditActor, actorName, subjectName)
	})
	status := "success"
	if err != nil {
		status = "error"
	}
	if s.metrics != nil {
		s.metrics.ObserveInfrastructure("security_log", "database", "persist", status, started)
	}
	return err
}

func (s *Service) validateConsumedEvent(envelope *commonv1.EventEnvelope, value payload) error {
	if envelope == nil || envelope.EventId == "" || len(envelope.EventId) > 256 || envelope.EventType != envelopeType || envelope.SchemaVersion != 1 || envelope.OccurredAt == nil || envelope.Context == nil {
		return fmt.Errorf("%w: invalid security log envelope", ErrInvalidEntry)
	}
	if err := envelope.OccurredAt.CheckValid(); err != nil || !envelope.OccurredAt.AsTime().Equal(value.OccurredAt) {
		return fmt.Errorf("%w: inconsistent security log occurrence time", ErrInvalidEntry)
	}
	if envelope.TenantId != value.TenantID || envelope.Context.ActorId != value.ActorID || envelope.Context.ActorType != value.ActorType || envelope.Context.TenantId != value.TenantID || envelope.Context.RequestId != value.RequestID || envelope.Context.TraceId != value.TraceID {
		return fmt.Errorf("%w: inconsistent security log envelope context", ErrInvalidEntry)
	}
	entry := Entry{EventType: value.EventType, SubjectID: value.SubjectID, SubjectName: value.SubjectName, SubjectType: value.SubjectType, TenantID: value.TenantID, SessionID: value.SessionID, Succeeded: value.Succeeded, Reason: value.Reason, ErrorCode: value.ErrorCode, ErrorMessage: value.ErrorMessage, ClientIP: value.ClientIP, UserAgent: value.UserAgent}
	if err := validateEntry(entry); err != nil || len(value.ActorID) > 256 || len(value.ActorType) > 64 || len(value.IdentifierHash) > 64 || len(value.TokenIDHash) > 64 || len(value.RequestID) > 256 || len(value.TraceID) > 64 || len(value.Metadata) > s.cfg.MaxPayloadBytes || !json.Valid(value.Metadata) {
		return fmt.Errorf("%w: invalid security log event payload", ErrInvalidEntry)
	}
	var metadata map[string]any
	if err := json.Unmarshal(value.Metadata, &metadata); err != nil || metadata == nil {
		return fmt.Errorf("%w: metadata must be a JSON object", ErrInvalidEntry)
	}
	return nil
}

func insert(ctx context.Context, tx *sqlx.Tx, id string, value payload, auditActor, actorName, subjectName string) error {
	now := time.Now()
	query := `INSERT INTO security_logs (id, tenant_id, actor_id, actor_name_snapshot, actor_type, subject_id, subject_name_snapshot, subject_type, event_type, succeeded, reason, error_code, error_message, identifier_hash, token_id_hash, session_id, request_id, trace_id, client_ip, user_agent, metadata, occurred_at, created_at, created_by, updated_at, updated_by, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CAST(? AS JSON), ?, ?, ?, ?, ?, ?)`
	if tx.DriverName() == "mysql" {
		query = "INSERT IGNORE" + strings.TrimPrefix(query, "INSERT")
	} else {
		query = strings.Replace(query, "CAST(? AS JSON)", "CAST(? AS jsonb)", 1) + " ON CONFLICT (id, occurred_at) DO NOTHING"
	}
	query = tx.Rebind(query)
	_, err := tx.ExecContext(ctx, query, id, value.TenantID, value.ActorID, truncate(actorName, 512), value.ActorType, value.SubjectID, truncate(subjectName, 512), value.SubjectType, value.EventType, value.Succeeded, value.Reason, value.ErrorCode, value.ErrorMessage, value.IdentifierHash, value.TokenIDHash, value.SessionID, value.RequestID, value.TraceID, value.ClientIP, value.UserAgent, string(value.Metadata), value.OccurredAt, now, auditActor, now, auditActor, 1)
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
	return value == EventLogin || value == EventTokenRefresh || value == EventLogout || value == EventForcedLogout || value == EventPasswordChanged || value == EventPasswordReset || value == EventSessionRevoked || value == EventLogoutAll || value == EventMembershipAdded || value == EventMembershipChanged || value == EventMembershipRemoved || value == EventPBACPolicyChanged || value == EventDataPolicyChanged || value == EventPermissionChanged || value == EventIdentityUserChanged || value == EventTenantChanged || value == EventTenantContextSwitch || value == EventApplicationContextSwitch || value == EventTenantAuthorization || value == EventMenuChanged || value == EventPlatformConfigChanged || value == EventSecurityLogAccess || value == EventServiceAccountChanged
}

func validateEntry(entry Entry) error {
	if !validEvent(entry.EventType) {
		return fmt.Errorf("%w: unsupported event type %q", ErrInvalidEntry, entry.EventType)
	}
	for name, value := range map[string]string{
		"subject_id": entry.SubjectID, "subject_name": entry.SubjectName, "subject_type": entry.SubjectType, "tenant_id": entry.TenantID,
		"identifier": entry.Identifier, "token_id": entry.TokenID, "session_id": entry.SessionID,
		"reason": entry.Reason, "error_code": entry.ErrorCode, "error_message": entry.ErrorMessage,
		"client_ip": entry.ClientIP, "user_agent": entry.UserAgent,
	} {
		if !utf8.ValidString(value) {
			return fmt.Errorf("%w: %s must be valid UTF-8", ErrInvalidEntry, name)
		}
	}
	if len(entry.SubjectID) > 256 || len(entry.SubjectName) > 512 || len(entry.SubjectType) > 64 || len(entry.TenantID) > 256 ||
		len(entry.Identifier) > 320 || len(entry.TokenID) > 4096 || len(entry.SessionID) > 256 ||
		len(entry.Reason) > 8192 || len(entry.ErrorCode) > 1024 || len(entry.ErrorMessage) > 8192 ||
		len(entry.ClientIP) > 256 || len(entry.UserAgent) > 4096 {
		return fmt.Errorf("%w: security log fields exceed their bounds", ErrInvalidEntry)
	}
	return nil
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
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, errors.New("metadata must be a JSON object")
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
