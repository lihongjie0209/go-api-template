package eventbus

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func completeOutboxEnvelope(id string) *commonv1.EventEnvelope {
	return &commonv1.EventEnvelope{
		EventId: id, EventType: "platform.event.v1", AggregateType: "test", SchemaVersion: 1,
		OccurredAt: timestamppb.Now(),
	}
}

func TestOutboxDispatchRecoversPanicAndRetries(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var output bytes.Buffer
		metrics := observability.NewMetrics(config.Config{Observability: config.Observability{MetricsEnabled: true}}, nil, nil)
		outbox := &Outbox{
			cfg:     config.EventBus{DispatchInterval: time.Second},
			logger:  slog.New(slog.NewJSONHandler(&output, nil)),
			metrics: metrics,
		}
		var attempts atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			outbox.runDispatchLoop(ctx, func(context.Context) error {
				if attempts.Add(1) == 1 {
					panic("token=secret-value")
				}
				cancel()
				return nil
			})
		}()
		time.Sleep(time.Second)
		synctest.Wait()
		<-done
		require.Equal(t, int32(2), attempts.Load())
		require.NotContains(t, output.String(), "secret-value")
		require.Contains(t, output.String(), "outbox_dispatch_panicked")
		metricResponse := httptest.NewRecorder()
		metrics.Handler().ServeHTTP(metricResponse, httptest.NewRequest("GET", "/metrics", nil))
		require.Contains(t, metricResponse.Body.String(), `event_outbox_dispatch_outcomes_total{outcome="panic_retry"} 1`)
	})
}

func TestOutboxDispatchDoesNotStartAfterCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	outbox := &Outbox{cfg: config.EventBus{DispatchInterval: time.Second}}
	attempts := 0
	outbox.runDispatchLoop(ctx, func(context.Context) error {
		attempts++
		return nil
	})
	require.Zero(t, attempts)
}

func TestOutboxDispatchEndsSpanWhenBatchPanics(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	previous := outboxTracer
	outboxTracer = provider.Tracer("outbox-panic-test")
	t.Cleanup(func() { outboxTracer = previous })
	outbox := &Outbox{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	envelope, err := proto.Marshal(completeOutboxEnvelope("event-1"))
	require.NoError(t, err)
	err = dispatchSafely(t.Context(), func(ctx context.Context) error {
		return outbox.dispatchRecord(ctx, outboxRecord{ID: "event-1", Subject: "platform.test", Envelope: envelope})
	})
	require.ErrorIs(t, err, errOutboxDispatchPanicked)
	require.Len(t, recorder.Ended(), 1)
	require.Equal(t, codes.Error, recorder.Ended()[0].Status().Code)
	require.Equal(t, "outbox dispatch panicked", recorder.Ended()[0].Status().Description)
}

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
	require.NoError(t, outbox.Store(ctx, tx, "platform.security.recorded.v1", completeOutboxEnvelope("event-1")))
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOutboxStoreRejectsIncompleteEventBeforeSQL(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	tx, err := db.BeginTxx(t.Context(), nil)
	require.NoError(t, err)
	outbox := &Outbox{cfg: config.EventBus{Enabled: true}, bus: &Bus{}}
	actorCtx := platformprincipal.SystemContext(t.Context(), "actor-1")
	for _, test := range []struct {
		name   string
		mutate func(*commonv1.EventEnvelope)
	}{
		{name: "missing event type", mutate: func(value *commonv1.EventEnvelope) { value.EventType = "" }},
		{name: "event type outside stream", mutate: func(value *commonv1.EventEnvelope) { value.EventType = "orders.created.v1" }},
		{name: "missing aggregate type", mutate: func(value *commonv1.EventEnvelope) { value.AggregateType = "" }},
		{name: "oversized aggregate type", mutate: func(value *commonv1.EventEnvelope) { value.AggregateType = strings.Repeat("a", 129) }},
		{name: "zero schema version", mutate: func(value *commonv1.EventEnvelope) { value.SchemaVersion = 0 }},
		{name: "missing occurrence time", mutate: func(value *commonv1.EventEnvelope) { value.OccurredAt = nil }},
		{name: "invalid occurrence time", mutate: func(value *commonv1.EventEnvelope) { value.OccurredAt = &timestamppb.Timestamp{Seconds: 253402300800} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			envelope := completeOutboxEnvelope("event-1")
			test.mutate(envelope)
			require.ErrorContains(t, outbox.Store(actorCtx, tx, "platform.event.v1", envelope), "invalid outbox envelope")
		})
	}
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDecodeClaimedEnvelopeRejectsIncompleteMetadata(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*commonv1.EventEnvelope)
	}{
		{name: "missing event type", mutate: func(value *commonv1.EventEnvelope) { value.EventType = "" }},
		{name: "event type outside stream", mutate: func(value *commonv1.EventEnvelope) { value.EventType = "orders.created.v1" }},
		{name: "missing aggregate type", mutate: func(value *commonv1.EventEnvelope) { value.AggregateType = "" }},
		{name: "oversized aggregate type", mutate: func(value *commonv1.EventEnvelope) { value.AggregateType = strings.Repeat("a", 129) }},
		{name: "zero schema version", mutate: func(value *commonv1.EventEnvelope) { value.SchemaVersion = 0 }},
		{name: "missing occurrence time", mutate: func(value *commonv1.EventEnvelope) { value.OccurredAt = nil }},
		{name: "invalid occurrence time", mutate: func(value *commonv1.EventEnvelope) { value.OccurredAt = &timestamppb.Timestamp{Seconds: 253402300800} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			envelope := completeOutboxEnvelope("event-1")
			test.mutate(envelope)
			encoded, err := proto.Marshal(envelope)
			require.NoError(t, err)
			_, err = decodeClaimedEnvelope(outboxRecord{ID: "event-1", Subject: "platform.event.v1", Envelope: encoded})
			require.ErrorIs(t, err, errDecodeEnvelope)
		})
	}
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
		{name: "outside configured stream", subject: "orders.created.v1", id: "event-1"},
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
			data, err := proto.Marshal(completeOutboxEnvelope(test.envelopeID))
			require.NoError(t, err)
			envelope, err := decodeClaimedEnvelope(outboxRecord{ID: test.storedID, Subject: "platform.event.v1", Envelope: data})
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
	_, err := decodeClaimedEnvelope(outboxRecord{ID: "event-1", Subject: "platform.event.v1", Envelope: []byte{0xff}})
	require.ErrorIs(t, err, errDecodeEnvelope)
}

func TestDecodeClaimedEnvelopeRejectsInvalidStoredRecord(t *testing.T) {
	t.Parallel()
	validEnvelope, err := proto.Marshal(completeOutboxEnvelope("event-1"))
	require.NoError(t, err)
	oversizedID := strings.Repeat("e", 65)
	oversizedIDEnvelope, err := proto.Marshal(completeOutboxEnvelope(oversizedID))
	require.NoError(t, err)
	large := completeOutboxEnvelope("event-1")
	large.Payload = make([]byte, maxOutboxEnvelopeBytes)
	oversizedEnvelope, err := proto.Marshal(large)
	require.NoError(t, err)
	require.Greater(t, len(oversizedEnvelope), maxOutboxEnvelopeBytes)
	for _, test := range []struct {
		name   string
		record outboxRecord
	}{
		{name: "outside configured stream", record: outboxRecord{ID: "event-1", Subject: "orders.created.v1", Envelope: validEnvelope}},
		{name: "wildcard subject", record: outboxRecord{ID: "event-1", Subject: "platform.>", Envelope: validEnvelope}},
		{name: "empty subject token", record: outboxRecord{ID: "event-1", Subject: "platform..event", Envelope: validEnvelope}},
		{name: "oversized subject", record: outboxRecord{ID: "event-1", Subject: strings.Repeat("s", 257), Envelope: validEnvelope}},
		{name: "oversized row ID", record: outboxRecord{ID: oversizedID, Subject: "platform.event.v1", Envelope: oversizedIDEnvelope}},
		{name: "oversized envelope", record: outboxRecord{ID: "event-1", Subject: "platform.event.v1", Envelope: oversizedEnvelope}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeClaimedEnvelope(test.record)
			require.ErrorIs(t, err, errDecodeEnvelope)
		})
	}
}

func TestOutboxDispatchDeadLettersMismatchedEnvelopeWithoutPublishing(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	storedEnvelope, err := proto.Marshal(&commonv1.EventEnvelope{EventId: "unexpected-event"})
	require.NoError(t, err)
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id,subject,envelope,trace_parent,trace_state,attempts,created_at FROM event_outbox`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "subject", "envelope", "trace_parent", "trace_state", "attempts", "created_at"}).
			AddRow("stored-event", "platform.test.recorded.v1", storedEnvelope, "", "", 0, now))
	mock.ExpectExec(`UPDATE event_outbox SET locked_by=`).
		WithArgs("worker-1", sqlmock.AnyArg(), sqlmock.AnyArg(), "api:outbox-dispatcher", "stored-event").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE event_outbox SET attempts=attempts\+1,dead_at=.* WHERE id=\? AND locked_by=\? AND published_at IS NULL AND deleted_at IS NULL$`).
		WithArgs(sqlmock.AnyArg(), "decode_failed", sqlmock.AnyArg(), "api:outbox-dispatcher", "stored-event", "worker-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	metrics := observability.NewMetrics(config.Config{Observability: config.Observability{MetricsEnabled: true}}, nil, nil)
	outbox := &Outbox{
		cfg:     config.EventBus{DispatchBatchSize: 1, DispatchLease: time.Minute, ConsumerMaxDeliver: 1},
		appName: "api", workerID: "worker-1", transactor: database.NewTransactor(sqlx.NewDb(db, "sqlmock")),
		metrics: metrics, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	require.NoError(t, outbox.dispatchBatch(t.Context()))
	require.NoError(t, mock.ExpectationsWereMet())
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	require.Contains(t, recorder.Body.String(), `event_outbox_dispatch_outcomes_total{outcome="dead_letter"} 1`)
}

func TestOutboxPublishFailureRemainsPendingAfterDeliveryLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE event_outbox SET attempts=attempts\+1,available_at=.* WHERE id=\? AND locked_by=\? AND published_at IS NULL AND deleted_at IS NULL$`).
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

func TestOutboxDecodeFailureMovesExhaustedEventToDeadState(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE event_outbox SET attempts=attempts\+1,dead_at=.* WHERE id=\? AND locked_by=\? AND published_at IS NULL AND deleted_at IS NULL$`).
		WithArgs(sqlmock.AnyArg(), "decode_failed", sqlmock.AnyArg(), "api:outbox-dispatcher", "event-1", "worker-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	outbox := &Outbox{
		cfg:     config.EventBus{ConsumerMaxDeliver: 2, DispatchRetryDelay: time.Second},
		appName: "api", workerID: "worker-1",
		transactor: database.NewTransactor(sqlx.NewDb(db, "sqlmock")),
	}
	require.NoError(t, outbox.release(t.Context(), "event-1", 1, errDecodeEnvelope))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOutboxReleaseStoresSafeDecodeReason(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	var scheduledAt capturedSQLTime
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE event_outbox SET attempts=attempts\+1,available_at=`).
		WithArgs(&scheduledAt, "decode_failed", sqlmock.AnyArg(), "api:outbox-dispatcher", "event-1", "worker-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	outbox := &Outbox{
		cfg:     config.EventBus{ConsumerMaxDeliver: 3, DispatchRetryDelay: time.Second, DispatchRetryMaxDelay: time.Minute},
		appName: "api", workerID: "worker-1",
		transactor: database.NewTransactor(sqlx.NewDb(db, "sqlmock")),
	}
	cause := errors.Join(errDecodeEnvelope, errors.New("secret-value from malformed payload"))
	started := time.Now()
	require.NoError(t, outbox.release(t.Context(), "event-1", 1, cause))
	finished := time.Now()
	require.False(t, scheduledAt.value.Before(started.Add(2*time.Second)))
	require.False(t, scheduledAt.value.After(finished.Add(2400*time.Millisecond)))
	require.NoError(t, mock.ExpectationsWereMet())
}

type capturedSQLTime struct{ value time.Time }

func (capture *capturedSQLTime) Match(value driver.Value) bool {
	when, ok := value.(time.Time)
	if ok {
		capture.value = when
	}
	return ok
}

func TestOutboxRetryBackoffIsExponentialJitteredAndCapped(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		base     time.Duration
		cap      time.Duration
		attempts int64
		minimum  time.Duration
		upper    time.Duration
	}{
		{name: "first failure", base: 2 * time.Second, cap: time.Minute, attempts: 0, minimum: 2 * time.Second, upper: 2400 * time.Millisecond},
		{name: "second failure", base: 2 * time.Second, cap: time.Minute, attempts: 1, minimum: 4 * time.Second, upper: 4800 * time.Millisecond},
		{name: "third failure", base: 2 * time.Second, cap: time.Minute, attempts: 2, minimum: 8 * time.Second, upper: 9600 * time.Millisecond},
		{name: "very large attempt count", base: 2 * time.Second, cap: time.Minute, attempts: 100, minimum: time.Minute, upper: time.Minute},
		{name: "maximum base", base: time.Hour, cap: time.Hour, attempts: 1, minimum: time.Hour, upper: time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for range 100 {
				delay := outboxRetryDelay(test.base, test.cap, test.attempts)
				require.GreaterOrEqual(t, delay, test.minimum)
				require.LessOrEqual(t, delay, test.upper)
			}
		})
	}
}
