package eventbus

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
)

func TestOutboxPersistenceErrorDoesNotLeakThroughLogOrTrace(t *testing.T) {
	t.Parallel()
	secretErr := errors.New("database URL with secret-value")
	var output bytes.Buffer
	outbox := &Outbox{logger: slog.New(slog.NewJSONHandler(&output, nil))}
	outbox.logDispatchFailure(t.Context(), secretErr)
	require.NotContains(t, output.String(), "secret-value")
	require.Contains(t, output.String(), "outbox_persistence_failed")

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	_, span := provider.Tracer("outbox-test").Start(t.Context(), "event_outbox.dispatch")
	recordOutboxPersistenceFailure(span, secretErr)
	span.End()
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Len(t, spans[0].Events(), 1)
	foundSafeCode := false
	for _, attribute := range spans[0].Events()[0].Attributes {
		message := attribute.Value.AsString()
		require.NotContains(t, message, "secret-value")
		if attribute.Key == "exception.message" && message == "outbox_persistence_failed" {
			foundSafeCode = true
		}
	}
	require.True(t, foundSafeCode, "trace event lacks safe failure code")
}

func TestOutboxStoreUsesCallerTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectBegin()
	tx, err := sqlxDB.BeginTxx(t.Context(), nil)
	require.NoError(t, err)
	mock.ExpectExec(`INSERT INTO event_outbox \(id,subject,envelope,trace_parent,trace_state,available_at,attempts,locked_by,last_error,`).
		WithArgs("event-1", "platform.security.recorded.v1", sqlmock.AnyArg(), "", "", sqlmock.AnyArg(), sqlmock.AnyArg(), "actor-1", sqlmock.AnyArg(), "actor-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectRollback()

	outbox := &Outbox{cfg: config.EventBus{Enabled: true}, bus: &Bus{}}
	ctx := platformprincipal.SystemContext(t.Context(), "actor-1")
	require.NoError(t, outbox.Store(ctx, tx, "platform.security.recorded.v1", &commonv1.EventEnvelope{EventId: "event-1"}))
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOutboxTraceContextRoundTrip(t *testing.T) {
	t.Parallel()
	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	state, err := trace.ParseTraceState("vendor=value")
	require.NoError(t, err)
	original := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, TraceState: state})
	ctx := trace.ContextWithSpanContext(context.Background(), original)
	traceParent, traceState := injectTraceContext(ctx)
	require.NotEmpty(t, traceParent)
	extracted := trace.SpanContextFromContext(extractTraceContext(context.Background(), traceParent, traceState))
	require.True(t, extracted.IsRemote())
	require.Equal(t, original.TraceID(), extracted.TraceID())
	require.Equal(t, original.SpanID(), extracted.SpanID())
	require.Equal(t, original.TraceState(), extracted.TraceState())
}

func TestOutboxStoreRejectsUnavailableDispatcher(t *testing.T) {
	outbox := &Outbox{}
	err := outbox.Store(t.Context(), nil, "subject", &commonv1.EventEnvelope{EventId: "event-1"})
	require.ErrorContains(t, err, "unavailable")
}

func TestOutboxStoreRejectsInvalidIdentityAndSubject(t *testing.T) {
	t.Parallel()
	outbox := &Outbox{cfg: config.EventBus{Enabled: true}, bus: &Bus{}}
	for _, test := range []struct {
		name    string
		subject string
		id      string
	}{
		{name: "wildcard", subject: "platform.>", id: "event-1"},
		{name: "empty token", subject: "platform..event", id: "event-1"},
		{name: "whitespace", subject: "platform.bad event", id: "event-1"},
		{name: "oversized id", subject: "platform.event.v1", id: strings.Repeat("e", 65)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := outbox.Store(platformprincipal.SystemContext(t.Context(), "actor"), &sqlx.Tx{}, test.subject, &commonv1.EventEnvelope{EventId: test.id})
			require.Error(t, err)
		})
	}
}

func TestDecodeClaimedEnvelopeRequiresStoredEventIdentity(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		storedID   string
		envelopeID string
		wantError  bool
	}{
		{name: "matching ID", storedID: "event-1", envelopeID: "event-1"},
		{name: "different ID", storedID: "event-1", envelopeID: "event-2", wantError: true},
		{name: "empty envelope ID", storedID: "event-1", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data, err := proto.Marshal(&commonv1.EventEnvelope{EventId: test.envelopeID})
			require.NoError(t, err)
			envelope, err := decodeClaimedEnvelope(outboxRecord{ID: test.storedID, Envelope: data})
			if test.wantError {
				require.ErrorIs(t, err, errDecodeEnvelope)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.storedID, envelope.EventId)
		})
	}
}

func TestDecodeClaimedEnvelopeRejectsMalformedProtobuf(t *testing.T) {
	t.Parallel()
	_, err := decodeClaimedEnvelope(outboxRecord{ID: "event-1", Envelope: []byte{0xff}})
	require.ErrorIs(t, err, errDecodeEnvelope)
}

func TestOutboxReleaseMovesExhaustedEventToDeadState(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE event_outbox SET attempts=attempts\+1,dead_at=`).
		WithArgs(sqlmock.AnyArg(), "publish_failed", sqlmock.AnyArg(), "api:outbox-dispatcher", "event-1", "worker-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	outbox := &Outbox{
		cfg:        config.EventBus{ConsumerMaxDeliver: 2, DispatchRetryDelay: time.Second},
		appName:    "api",
		workerID:   "worker-1",
		transactor: database.NewTransactor(sqlxDB),
	}
	require.NoError(t, outbox.release(t.Context(), "event-1", 1, errors.New("publish failed: bearer secret-value")))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOutboxReleaseStoresSafeDecodeReason(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE event_outbox SET attempts=attempts\+1,available_at=`).
		WithArgs(sqlmock.AnyArg(), "decode_failed", sqlmock.AnyArg(), "api:outbox-dispatcher", "event-1", "worker-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	outbox := &Outbox{
		cfg:     config.EventBus{ConsumerMaxDeliver: 3, DispatchRetryDelay: time.Second},
		appName: "api", workerID: "worker-1",
		transactor: database.NewTransactor(sqlx.NewDb(db, "sqlmock")),
	}
	cause := errors.Join(errDecodeEnvelope, errors.New("secret-value from malformed payload"))
	require.NoError(t, outbox.release(t.Context(), "event-1", 0, cause))
	require.NoError(t, mock.ExpectationsWereMet())
}
