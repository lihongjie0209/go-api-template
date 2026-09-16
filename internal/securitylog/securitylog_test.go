package securitylog

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
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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

func TestHashIsKeyedAndStable(t *testing.T) {
	t.Parallel()
	first := (&Service{cfg: config.SecurityLog{HashKey: strings.Repeat("a", 32)}}).hash("alice@example.com")
	second := (&Service{cfg: config.SecurityLog{HashKey: strings.Repeat("b", 32)}}).hash("alice@example.com")
	if first == "" || first == second || strings.Contains(first, "alice") {
		t.Fatalf("hashes = %q %q", first, second)
	}
}

func TestIdentifierHashNormalizesCaseAndWhitespace(t *testing.T) {
	t.Parallel()
	service := &Service{cfg: config.SecurityLog{HashKey: strings.Repeat("a", 32)}}
	if service.hashIdentifier(" Alice@Example.COM ") != service.hashIdentifier("alice@example.com") {
		t.Fatal("identifier hashes differ after normalization")
	}
	if service.hash(" Refresh-Token ") == service.hash("refresh-token") {
		t.Fatal("opaque token hashing unexpectedly normalized token")
	}
}

func TestSafeMetadataRejectsCredentials(t *testing.T) {
	t.Parallel()
	for _, metadata := range []map[string]any{{"access_token": "secret"}, {"nested": map[string]any{"client_secret": "secret"}}, {"api-key": "secret"}} {
		if _, err := safeMetadata(metadata, 1024); err == nil {
			t.Fatalf("credential metadata error = nil for %#v", metadata)
		}
	}
	if value, err := safeMetadata(map[string]any{"device": "mobile"}, 1024); err != nil || string(value) != `{"device":"mobile"}` {
		t.Fatalf("metadata = %s, %v", value, err)
	}
	if _, err := safeMetadata([]string{"not", "an", "object"}, 1024); err == nil {
		t.Fatal("non-object metadata error = nil")
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
		cfg:    config.SecurityLog{Enabled: true, Subject: "platform.security-log.v1", MaxPayloadBytes: 1024, HashKey: strings.Repeat("h", 32)},
		outbox: outbox, transactor: database.NewTransactor(sqlxDB),
	}
	base := platformprincipal.SystemContext(context.Background(), "actor-1")
	canceled, cancel := context.WithCancel(base)
	cancel()
	if err := service.Record(canceled, Entry{EventType: EventLogin, SubjectID: "user-1", Succeeded: true}); err != nil {
		t.Fatal(err)
	}
	if !outbox.called || outbox.subject != "platform.security-log.v1" || outbox.event == nil || outbox.event.Context.GetActorId() != "actor-1" {
		t.Fatalf("outbox = called:%v subject:%q event:%+v", outbox.called, outbox.subject, outbox.event)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordSupportsAnonymousLoginBeforeUserContextExists(t *testing.T) {
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
		cfg:     config.SecurityLog{Enabled: true, Subject: "platform.security-log.v1", MaxPayloadBytes: 1024, HashKey: strings.Repeat("h", 32)},
		appName: "identity-service", outbox: outbox, transactor: database.NewTransactor(sqlxDB),
	}
	if err := service.Record(t.Context(), Entry{EventType: EventLogin, Identifier: "alice", Succeeded: false}); err != nil {
		t.Fatal(err)
	}
	if !outbox.called || outbox.event == nil || outbox.event.Context.GetActorId() != "" {
		t.Fatalf("anonymous login event = %+v", outbox.event)
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
	service := &Service{cfg: config.SecurityLog{Enabled: true, Subject: "platform.security-log.v1", MaxPayloadBytes: 1024, HashKey: strings.Repeat("h", 32)}, outbox: outbox}
	if err := service.RecordTx(platformprincipal.SystemContext(t.Context(), "actor-1"), tx, Entry{EventType: EventLogin, Succeeded: true}); err != nil {
		t.Fatal(err)
	}
	if !outbox.called {
		t.Fatal("RecordTx() did not use caller transaction")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordTxEnrichesClientContext(t *testing.T) {
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
	service := &Service{cfg: config.SecurityLog{Enabled: true, Subject: "platform.security-log.v1", MaxPayloadBytes: 1024, HashKey: strings.Repeat("h", 32)}, outbox: outbox}
	ctx := WithClient(platformprincipal.SystemContext(t.Context(), "actor-1"), "203.0.113.10", "integration-agent")
	if err := service.RecordTx(ctx, tx, Entry{EventType: EventLogin, Succeeded: true}); err != nil {
		t.Fatal(err)
	}
	var recorded payload
	if err := json.Unmarshal(outbox.event.Payload, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.ClientIP != "203.0.113.10" || recorded.UserAgent != "integration-agent" {
		t.Fatalf("client context = %q/%q", recorded.ClientIP, recorded.UserAgent)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateConsumedEventRejectsEnvelopeContextMismatch(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	value := payload{EventType: EventLogin, ActorID: "actor-1", ActorType: "user", TenantID: "tenant-1", Metadata: []byte(`{}`), OccurredAt: now}
	envelope := &commonv1.EventEnvelope{EventId: "event-1", EventType: envelopeType, SchemaVersion: 1, TenantId: "tenant-2", OccurredAt: timestamppb.New(now), Context: &commonv1.RequestContext{ActorId: "actor-1", ActorType: "user", TenantId: "tenant-1"}}
	service := &Service{cfg: config.SecurityLog{MaxPayloadBytes: 1024}}
	if err := service.validateConsumedEvent(envelope, value); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("validateConsumedEvent() = %v", err)
	}
}

func TestValidateEntryRejectsUnboundedFields(t *testing.T) {
	t.Parallel()
	if err := validateEntry(Entry{EventType: EventLogin, TokenID: strings.Repeat("x", 4097)}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("validateEntry() = %v", err)
	}
}
