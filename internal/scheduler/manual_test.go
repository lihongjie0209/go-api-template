package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"github.com/stretchr/testify/require"
)

type manualOutboxStub struct {
	store func(context.Context, *sqlx.Tx, string, *commonv1.EventEnvelope) error
}

func (s manualOutboxStub) Store(ctx context.Context, tx *sqlx.Tx, subject string, event *commonv1.EventEnvelope) error {
	return s.store(ctx, tx, subject, event)
}

func TestTriggerQueuesRunAndEventInOneTransaction(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	columns := []string{"id", "code", "name", "description", "cron_spec", "timezone", "handler", "timeout_seconds", "lock_ttl_seconds", "status", "payload", "created_at", "created_by", "updated_at", "updated_by", "version"}
	mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=`).WithArgs("job-1").WillReturnRows(sqlmock.NewRows(columns).AddRow("job-1", "sample-job", "Sample", "", "0 * * * * *", "Asia/Shanghai", "system.sample", 3600, 60, "active", []byte(`{"value":1}`), now, "actor", now, "actor", 1))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO scheduled_job_runs`).WithArgs(sqlmock.AnyArg(), "job-1", "sample-job", "Sample", "system.sample", "manual", "queued", "req-1", sqlmock.AnyArg(), `{"value":1}`, int64(3600), int64(60), sqlmock.AnyArg(), "actor", sqlmock.AnyArg(), "actor").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	manager := &Manager{manualWake: make(chan struct{}, 1)}
	service := &DefinitionService{db: db, tx: database.NewTransactor(db), runtime: manager, outbox: manualOutboxStub{store: func(_ context.Context, _ *sqlx.Tx, subject string, event *commonv1.EventEnvelope) error {
		require.Equal(t, manualDispatchEventType, subject)
		require.Equal(t, "req-1", event.Context.RequestId)
		require.Equal(t, event.EventId, stringValue(t, event.Payload, "run_id"))
		return nil
	}}}
	ctx := requestid.WithContext(platformprincipal.SystemContext(t.Context(), "actor"), "req-1")
	result, err := service.Trigger(ctx, "job-1")
	require.NoError(t, err)
	require.Equal(t, "queued", result.Status)
	require.NotEmpty(t, result.RunID)
	require.Empty(t, manager.manualWake, "worker must wait for durable event delivery")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTriggerRollsBackWhenDispatchCannotBeStored(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	columns := []string{"id", "code", "name", "description", "cron_spec", "timezone", "handler", "timeout_seconds", "lock_ttl_seconds", "status", "payload", "created_at", "created_by", "updated_at", "updated_by", "version"}
	mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=`).WithArgs("job-1").WillReturnRows(sqlmock.NewRows(columns).AddRow("job-1", "sample-job", "Sample", "", "0 * * * * *", "Asia/Shanghai", "system.sample", 30, 60, "active", []byte(`{}`), now, "actor", now, "actor", 1))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO scheduled_job_runs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectRollback()
	wake := make(chan struct{}, 1)
	service := &DefinitionService{db: db, tx: database.NewTransactor(db), runtime: &Manager{manualWake: wake}, outbox: manualOutboxStub{store: func(context.Context, *sqlx.Tx, string, *commonv1.EventEnvelope) error {
		return errors.New("outbox unavailable")
	}}}
	result, err := service.Trigger(platformprincipal.SystemContext(t.Context(), "actor"), "job-1")
	require.ErrorContains(t, err, "outbox unavailable")
	require.Empty(t, result.RunID)
	require.Empty(t, wake)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTriggerRequiresPrincipalAndExistingDefinition(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &DefinitionService{db: sqlx.NewDb(raw, "sqlmock")}
	result, err := service.Trigger(t.Context(), "job-1")
	require.Error(t, err)
	require.Empty(t, result.RunID)
	mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=`).WithArgs("missing").WillReturnError(sql.ErrNoRows)
	result, err = service.Trigger(platformprincipal.SystemContext(t.Context(), "actor"), "missing")
	require.ErrorIs(t, err, ErrDefinitionNotFound)
	require.Empty(t, result.RunID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConsumeManualOnlyWakesForMatchingRunID(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	manager := &Manager{cfg: config.Config{App: config.App{Name: "orders-service"}}, tx: database.NewTransactor(sqlx.NewDb(raw, "sqlmock")), manualWake: make(chan struct{}, 1)}
	for _, event := range []*commonv1.EventEnvelope{
		{EventId: "run-1", EventType: manualDispatchEventType, SchemaVersion: 1, Payload: []byte(`{"run_id":"run-2"}`)},
		{EventId: "run-1", EventType: manualDispatchEventType, SchemaVersion: 2, Payload: []byte(`{"run_id":"run-1"}`)},
	} {
		require.Error(t, manager.consumeManual(t.Context(), event))
	}
	require.Empty(t, manager.manualWake)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE scheduled_job_runs SET dispatched_at=`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "orders-service:scheduler:dispatcher", "run-1", "job-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, manager.consumeManual(t.Context(), &commonv1.EventEnvelope{EventId: "run-1", EventType: manualDispatchEventType, AggregateId: "job-1", SchemaVersion: 1, Payload: []byte(`{"run_id":"run-1"}`)}))
	require.Len(t, manager.manualWake, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConsumeManualAcknowledgesDuplicateWithoutRequeue(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	manager := &Manager{cfg: config.Config{App: config.App{Name: "orders-service"}}, tx: database.NewTransactor(sqlx.NewDb(raw, "sqlmock")), manualWake: make(chan struct{}, 1)}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE scheduled_job_runs SET dispatched_at=`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id FROM scheduled_job_runs WHERE id=`).WithArgs("run-1", "job-1").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("run-1"))
	mock.ExpectCommit()
	event := &commonv1.EventEnvelope{EventId: "run-1", EventType: manualDispatchEventType, AggregateId: "job-1", SchemaVersion: 1, Payload: []byte(`{"run_id":"run-1"}`)}
	require.NoError(t, manager.consumeManual(t.Context(), event))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClaimManualOnlySelectsDispatchedOrExpiredRun(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	manager := &Manager{cfg: config.Config{App: config.App{Name: "orders-service"}}, tx: database.NewTransactor(sqlx.NewDb(raw, "sqlmock"))}
	columns := []string{"id", "scheduled_job_id", "job_code", "job_name", "handler", "payload", "timeout_seconds", "lock_ttl_seconds", "request_id"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM scheduled_job_runs WHERE .*dispatched_at IS NOT NULL`).WillReturnRows(sqlmock.NewRows(columns).AddRow("run-1", "job-1", "job-code", "Job", "system.sample", "{}", 30, 60, "req-1"))
	mock.ExpectExec(`UPDATE scheduled_job_runs SET status='running'.*dispatched_at IS NOT NULL`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "orders-service:scheduler:manual", "run-1", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	run, found, err := manager.claimManual(t.Context())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "run-1", run.ID)
	require.NotEmpty(t, run.LeaseOwner)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClaimManualRecoversExpiredRun(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	manager := &Manager{cfg: config.Config{App: config.App{Name: "orders-service"}}, tx: database.NewTransactor(sqlx.NewDb(raw, "sqlmock"))}
	columns := []string{"id", "scheduled_job_id", "job_code", "job_name", "handler", "payload", "timeout_seconds", "lock_ttl_seconds", "request_id"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* status='queued' AND dispatched_at IS NOT NULL`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* status='running' AND lease_until<`).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(columns).AddRow("run-1", "job-1", "job-code", "Job", "system.sample", "{}", 30, 60, "req-1"))
	mock.ExpectExec(`UPDATE scheduled_job_runs SET status='running'`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	run, found, err := manager.claimManual(t.Context())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "run-1", run.ID)
	require.NotEmpty(t, run.LeaseOwner)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFinishManualRejectsLostLease(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	manager := &Manager{
		cfg:    config.Config{App: config.App{Name: "orders-service"}},
		tx:     database.NewTransactor(db),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		operations: runLogRecorderStub{recordTx: func(context.Context, *sqlx.Tx, operationlog.Entry) error {
			t.Fatal("lost lease must not emit completion log")
			return nil
		}},
	}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE scheduled_job_runs SET status=`).WithArgs("success", sqlmock.AnyArg(), sqlmock.AnyArg(), "", sqlmock.AnyArg(), "orders-service:scheduler:manual", "run-1", "worker-1", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	require.ErrorContains(t, manager.finishManual(t.Context(), manualRun{ID: "run-1", LeaseOwner: "worker-1"}, "success", time.Now()), "lease was lost")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExecuteManualFinishesOutsideHTTPContext(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := &Manager{
		cfg:      config.Config{App: config.App{Name: "orders-service"}},
		tx:       database.NewTransactor(db),
		locker:   lockerStub{mutex: mutexStub{}, acquired: true},
		metrics:  &observability.Metrics{},
		logger:   logger,
		handlers: NewHandlerRegistry(logger),
		operations: runLogRecorderStub{recordTx: func(ctx context.Context, tx *sqlx.Tx, entry operationlog.Entry) error {
			require.NotNil(t, tx)
			require.Equal(t, "req-1", mustRequestID(t, ctx))
			require.Equal(t, "run-1", entry.ResourceID)
			require.Equal(t, "manual", entry.Request.(map[string]any)["trigger_source"])
			return nil
		}},
	}
	manager.handlers.handlers["system.sample"] = HandlerDefinition{Key: "system.sample", handler: func(ctx context.Context, _ json.RawMessage) error {
		runID, ok := RunIDFromContext(ctx)
		require.True(t, ok)
		require.Equal(t, "run-1", runID)
		requestID, ok := requestid.FromContext(ctx)
		require.True(t, ok)
		require.Equal(t, "req-1", requestID)
		return nil
	}}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE scheduled_job_runs SET status=`).WithArgs("success", sqlmock.AnyArg(), sqlmock.AnyArg(), "", sqlmock.AnyArg(), "orders-service:scheduler:manual", "run-1", "lease-1", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	cancelRequest()
	require.ErrorIs(t, requestCtx.Err(), context.Canceled)
	manager.executeManual(context.WithoutCancel(requestCtx), manualRun{ID: "run-1", JobCode: "sample-job", Handler: "system.sample", Payload: "{}", TimeoutSeconds: 30, LockTTLSeconds: 60, RequestID: "req-1", LeaseOwner: "lease-1"})
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFinishManualRollsBackWhenOperationLogCannotBeQueued(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	manager := &Manager{
		cfg: config.Config{App: config.App{Name: "orders-service"}},
		tx:  database.NewTransactor(sqlx.NewDb(raw, "sqlmock")),
		operations: runLogRecorderStub{recordTx: func(context.Context, *sqlx.Tx, operationlog.Entry) error {
			return errors.New("outbox unavailable")
		}},
	}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE scheduled_job_runs SET status=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()
	err = manager.finishManual(requestid.WithContext(t.Context(), "req-1"), manualRun{ID: "run-1", LeaseOwner: "worker-1"}, "success", time.Now())
	require.ErrorContains(t, err, "outbox unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRenewManualRejectsLostOwnership(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	manager := &Manager{cfg: config.Config{App: config.App{Name: "orders-service"}}, tx: database.NewTransactor(sqlx.NewDb(raw, "sqlmock"))}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE scheduled_job_runs SET lease_until=`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "orders-service:scheduler:manual", "run-1", "stale-owner", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	require.ErrorContains(t, manager.renewManual(t.Context(), "run-1", "stale-owner"), "lease was lost")
	require.NoError(t, mock.ExpectationsWereMet())
}

func stringValue(t *testing.T, data []byte, key string) string {
	t.Helper()
	var value map[string]string
	require.NoError(t, json.Unmarshal(data, &value))
	return value[key]
}
