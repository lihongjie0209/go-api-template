package eventbus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestOutboxStoreUsesCallerTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectBegin()
	tx, err := sqlxDB.BeginTxx(t.Context(), nil)
	require.NoError(t, err)
	mock.ExpectExec(`INSERT INTO event_outbox`).
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

func TestOutboxReleaseMovesExhaustedEventToDeadState(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE event_outbox SET attempts=attempts\+1,dead_at=`).
		WithArgs(sqlmock.AnyArg(), "publish failed", sqlmock.AnyArg(), "api:outbox-dispatcher", "event-1", "worker-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	outbox := &Outbox{
		cfg:        config.EventBus{ConsumerMaxDeliver: 2, DispatchRetryDelay: time.Second},
		appName:    "api",
		workerID:   "worker-1",
		transactor: database.NewTransactor(sqlxDB),
	}
	require.NoError(t, outbox.release(t.Context(), "event-1", 1, errors.New("publish failed")))
	require.NoError(t, mock.ExpectationsWereMet())
}
