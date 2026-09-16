package operationlog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type recorderStub struct{ entry Entry }

type outboxStub struct {
	called  bool
	subject string
	event   *commonv1.EventEnvelope
}

func (s *outboxStub) Store(_ context.Context, tx *sqlx.Tx, subject string, event *commonv1.EventEnvelope) error {
	s.called = tx != nil
	s.subject = subject
	s.event = event
	return nil
}

func (r *recorderStub) Enabled() bool { return true }

func (r *recorderStub) Record(_ context.Context, entry Entry) error { r.entry = entry; return nil }

func TestSanitizedJSONRedactsAndTruncates(t *testing.T) {
	t.Parallel()
	value, err := sanitizedJSON(map[string]any{
		"username": "alice",
		"password": "plain",
		"nested":   map[string]any{"access_token": "token", "value": strings.Repeat("x", 100)},
	}, 80)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(value, "plain") || strings.Contains(value, `:"token"`) {
		t.Fatalf("sanitized payload leaked secret: %s", value)
	}
	if len([]rune(value)) > 80 {
		t.Fatalf("payload length = %d", len([]rune(value)))
	}
	if !json.Valid([]byte(value)) {
		t.Fatalf("truncated payload is not valid JSON: %s", value)
	}
}

func TestValidateConsumedEventRejectsEnvelopeContextMismatch(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	payload := eventPayload{
		Entry:      Entry{Operation: "order.create", Source: "backend", Protocol: "service", Succeeded: true},
		DurationMS: 1, ActorID: "actor-1", ActorType: "user", TenantID: "tenant-1",
		OccurredAt: now, Extension: json.RawMessage(`{}`),
	}
	envelope := &commonv1.EventEnvelope{
		EventId: "event-1", EventType: eventType, SchemaVersion: 1,
		TenantId: "tenant-2", OccurredAt: timestamppb.New(now),
		Context: &commonv1.RequestContext{ActorId: "actor-1", ActorType: "user", TenantId: "tenant-1"},
	}
	service := &Service{cfg: config.OperationLog{MaxPayloadBytes: 1024}}
	if err := service.validateConsumedEvent(envelope, payload); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("validateConsumedEvent() = %v", err)
	}
}

func TestValidateEntryRejectsUnboundedFields(t *testing.T) {
	t.Parallel()
	entry := Entry{Operation: strings.Repeat("x", 257), Source: "backend", Protocol: "service"}
	if err := validateEntry(entry); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("validateEntry() = %v", err)
	}
}

func TestDoMeasuresResult(t *testing.T) {
	t.Parallel()
	recorder := &recorderStub{}
	want := errors.New("business failed")
	err := Do(t.Context(), recorder, Entry{Operation: "test", Protocol: "unit"}, func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("Do() error = %v", err)
	}
	if recorder.entry.Succeeded || recorder.entry.Duration < 0 || recorder.entry.ErrorCode != "operation_failed" || recorder.entry.ErrorMessage != "operation failed" {
		t.Fatalf("recorded entry = %+v", recorder.entry)
	}
}

func TestSanitizedJSONPreservesOrdinaryFields(t *testing.T) {
	t.Parallel()
	value, err := sanitizedJSON(map[string]any{"id": "42", "action": "update"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(value, `"id":"42"`) {
		t.Fatalf("payload = %s", value)
	}
}

func TestSanitizedRawJSONRemainsValidAndBounded(t *testing.T) {
	t.Parallel()
	value, err := sanitizedRawJSON(map[string]any{"secret": "hidden", "menu": "users"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(value) || strings.Contains(string(value), "hidden") {
		t.Fatalf("value = %s", value)
	}
	if _, err := sanitizedRawJSON(map[string]any{"value": strings.Repeat("x", 100)}, 10); err == nil {
		t.Fatal("oversized JSON error = nil")
	}
}

func TestRecordDurablyStoresOutboxEventAfterRequestCancellation(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectCommit()
	outbox := &outboxStub{}
	service := &Service{
		enabled: true,
		cfg:     config.OperationLog{Subject: "platform.operation-log.v1", MaxPayloadBytes: 1024},
		appName: "orders-service", outbox: outbox,
		transactor: database.NewTransactor(sqlxDB),
		metrics:    observability.NewMetrics(config.Config{}, nil, nil),
	}
	base := platformprincipal.SystemContext(context.Background(), "actor-1")
	canceled, cancel := context.WithCancel(base)
	cancel()

	err = service.Record(canceled, Entry{Operation: "order.create", Source: "backend", Protocol: "service", Duration: time.Millisecond, Succeeded: true})
	if err != nil {
		t.Fatal(err)
	}
	if !outbox.called || outbox.subject != "platform.operation-log.v1" || outbox.event == nil || outbox.event.Context.GetActorId() != "actor-1" {
		t.Fatalf("outbox = called:%v subject:%q event:%+v", outbox.called, outbox.subject, outbox.event)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordTxUsesCallerTransaction(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectBegin()
	tx, err := sqlxDB.BeginTxx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectRollback()
	outbox := &outboxStub{}
	service := &Service{enabled: true, cfg: config.OperationLog{Subject: "platform.operation-log.v1", MaxPayloadBytes: 1024}, outbox: outbox}
	ctx := platformprincipal.SystemContext(t.Context(), "actor-1")
	if err := service.RecordTx(ctx, tx, Entry{Operation: "order.create", ResourceID: "order-1", ResourceName: "Quarterly Order", Source: "backend", Protocol: "service", Succeeded: true}); err != nil {
		t.Fatal(err)
	}
	if !outbox.called {
		t.Fatal("RecordTx() did not use caller transaction")
	}
	var payload eventPayload
	if err := json.Unmarshal(outbox.event.Payload, &payload); err != nil || payload.ResourceName != "Quarterly Order" {
		t.Fatalf("payload=%+v error=%v", payload, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
