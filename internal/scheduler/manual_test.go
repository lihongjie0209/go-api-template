package scheduler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
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
	"go.opentelemetry.io/otel/trace"
)

type manualOutboxStub struct {
	store func(context.Context, *sqlx.Tx, string, *commonv1.EventEnvelope) error
}

type manualFailureLogHandler struct {
	message   string
	requestID string
	traceID   string
}

func (h *manualFailureLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *manualFailureLogHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "finish manual scheduled job" {
		h.message = record.Message
		h.requestID, _ = requestid.FromContext(ctx)
		h.traceID = trace.SpanContextFromContext(ctx).TraceID().String()
	}
	return nil
}
func (h *manualFailureLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *manualFailureLogHandler) WithGroup(string) slog.Handler      { return h }

func (s manualOutboxStub) Store(ctx context.Context, tx *sqlx.Tx, subject string, event *commonv1.EventEnvelope) error {
	return s.store(ctx, tx, subject, event)
}

func TestValidateManualRunRejectsInvalidPersistedSnapshot(t *testing.T) {
	t.Parallel()
	valid := manualRun{JobCode: "sample-job", JobName: "Sample", Handler: "system.sample", Payload: `{}`, TimeoutSeconds: 3600, LockTTLSeconds: 86400}
	require.NoError(t, validateManualRun(valid))
	boundary := valid
	boundary.Payload = `{"data":"` + strings.Repeat("x", (1<<20)-len(`{"data":""}`)) + `"}`
	require.Len(t, boundary.Payload, 1<<20)
	require.NoError(t, validateManualRun(boundary))
	for _, test := range []struct {
		name   string
		change func(*manualRun)
	}{
		{name: "unbounded timeout", change: func(run *manualRun) { run.TimeoutSeconds = 3601 }},
		{name: "negative timeout", change: func(run *manualRun) { run.TimeoutSeconds = -1 }},
		{name: "unbounded lock lease", change: func(run *manualRun) { run.LockTTLSeconds = 86401 }},
		{name: "invalid payload", change: func(run *manualRun) { run.Payload = `[]` }},
		{name: "oversized payload", change: func(run *manualRun) { run.Payload = `{"data":"` + strings.Repeat("x", 1<<20) + `"}` }},
		{name: "invalid job code", change: func(run *manualRun) { run.JobCode = "BAD CODE" }},
		{name: "missing name", change: func(run *manualRun) { run.JobName = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := valid
			test.change(&run)
			require.ErrorIs(t, validateManualRun(run), ErrInvalidDefinition)
		})
	}
}

func TestExecuteManualRejectsInvalidSnapshotWithoutCallingHandler(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	called := false
	metrics := observability.NewMetrics(config.Config{App: config.App{Name: "orders-service"}, Observability: config.Observability{MetricsEnabled: true}}, nil, nil)
	manager := &Manager{
		cfg:      config.Config{App: config.App{Name: "orders-service"}},
		tx:       database.NewTransactor(sqlx.NewDb(raw, "sqlmock")),
		metrics:  metrics,
		logger:   logger,
		handlers: NewHandlerRegistry(logger),
		operations: runLogRecorderStub{recordTx: func(_ context.Context, _ *sqlx.Tx, entry operationlog.Entry) error {
			require.Equal(t, "Invalid scheduled job snapshot", entry.ResourceName)
			require.Equal(t, "invalid.snapshot", entry.Request.(map[string]any)["handler"])
			return nil
		}},
	}
	manager.handlers.handlers["system.sample"] = HandlerDefinition{Key: "system.sample", handler: func(context.Context, json.RawMessage) error {
		called = true
		return nil
	}}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE scheduled_job_runs SET status=`).WithArgs("error", sqlmock.AnyArg(), sqlmock.AnyArg(), "scheduled job execution failed", sqlmock.AnyArg(), "orders-service:scheduler:manual", "run-1", "lease-1", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	manager.executeManual(t.Context(), manualRun{ID: "run-1", JobCode: "sample-job", JobName: strings.Repeat("x", 600), Handler: "system.sample", Payload: `{}`, TimeoutSeconds: 1 << 62, LockTTLSeconds: 60, LeaseOwner: "lease-1"})
	require.False(t, called)
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	require.Contains(t, response.Body.String(), `cron_runs_total{job="invalid.snapshot",status="error"} 1`)
	require.NoError(t, mock.ExpectationsWereMet())
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
	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled})
	mock.ExpectExec(`INSERT INTO scheduled_job_runs`).WithArgs(sqlmock.AnyArg(), "job-1", "sample-job", "Sample", "system.sample", "manual", "queued", "req-1", traceID.String(), "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01", "", `{"value":1}`, int64(3600), int64(60), sqlmock.AnyArg(), "actor", sqlmock.AnyArg(), "actor").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	manager := &Manager{manualWake: make(chan struct{}, 1)}
	service := &DefinitionService{db: db, tx: database.NewTransactor(db), runtime: manager, outbox: manualOutboxStub{store: func(_ context.Context, _ *sqlx.Tx, subject string, event *commonv1.EventEnvelope) error {
		require.Equal(t, manualDispatchEventType, subject)
		require.Equal(t, "req-1", event.Context.RequestId)
		require.Equal(t, event.EventId, stringValue(t, event.Payload, "run_id"))
		return nil
	}}}
	ctx := requestid.WithContext(platformprincipal.SystemContext(trace.ContextWithSpanContext(t.Context(), spanContext), "actor"), "req-1")
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
	run, found, err := manager.claimManual(t.Context(), false)
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
	columns := []string{"id", "scheduled_job_id", "job_code", "job_name", "handler", "payload", "timeout_seconds", "lock_ttl_seconds", "request_id", "trace_parent", "trace_state"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* status='queued' AND dispatched_at IS NOT NULL`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* status='running' AND lease_until<`).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(columns).AddRow("run-1", "job-1", "job-code", "Job", "system.sample", "{}", 30, 60, "req-1", "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01", "vendor=value"))
	mock.ExpectExec(`UPDATE scheduled_job_runs SET status='running'`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	run, found, err := manager.claimManual(t.Context(), false)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "run-1", run.ID)
	require.Equal(t, "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01", run.TraceParent)
	require.Equal(t, "vendor=value", run.TraceState)
	require.NotEmpty(t, run.LeaseOwner)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClaimManualPrefersExpiredRunWhileQueuedRunsExist(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	manager := &Manager{cfg: config.Config{App: config.App{Name: "orders-service"}}, tx: database.NewTransactor(sqlx.NewDb(raw, "sqlmock"))}
	columns := []string{"id", "scheduled_job_id", "job_code", "job_name", "handler", "payload", "timeout_seconds", "lock_ttl_seconds", "request_id"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* status='running' AND lease_until<`).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(columns).AddRow("expired-run", "job-1", "job-code", "Job", "system.sample", "{}", 30, 60, "req-1"))
	mock.ExpectExec(`UPDATE scheduled_job_runs SET status='running'`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	run, found, err := manager.claimManual(t.Context(), true)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "expired-run", run.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClaimManualFallsBackToQueuedRunWhenNoLeaseExpired(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	manager := &Manager{cfg: config.Config{App: config.App{Name: "orders-service"}}, tx: database.NewTransactor(sqlx.NewDb(raw, "sqlmock"))}
	columns := []string{"id", "scheduled_job_id", "job_code", "job_name", "handler", "payload", "timeout_seconds", "lock_ttl_seconds", "request_id"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* status='running' AND lease_until<`).WithArgs(sqlmock.AnyArg()).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* status='queued' AND dispatched_at IS NOT NULL`).WillReturnRows(sqlmock.NewRows(columns).AddRow("queued-run", "job-1", "job-code", "Job", "system.sample", "{}", 30, 60, "req-1"))
	mock.ExpectExec(`UPDATE scheduled_job_runs SET status='running'`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	run, found, err := manager.claimManual(t.Context(), true)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "queued-run", run.ID)
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
	locker := &contendingLocker{mutex: mutexStub{}, contention: 1}
	manager := &Manager{
		cfg:      config.Config{App: config.App{Name: "orders-service"}, DistributedLock: config.DistributedLock{RetryDelay: time.Millisecond}},
		tx:       database.NewTransactor(db),
		locker:   locker,
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
		require.Equal(t, "0102030405060708090a0b0c0d0e0f10", trace.SpanContextFromContext(ctx).TraceID().String())
		return nil
	}}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE scheduled_job_runs SET status=`).WithArgs("success", sqlmock.AnyArg(), sqlmock.AnyArg(), "", sqlmock.AnyArg(), "orders-service:scheduler:manual", "run-1", "lease-1", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	cancelRequest()
	require.ErrorIs(t, requestCtx.Err(), context.Canceled)
	manager.executeManual(context.WithoutCancel(requestCtx), manualRun{ID: "run-1", JobCode: "sample-job", JobName: "Sample", Handler: "system.sample", Payload: "{}", TimeoutSeconds: 30, LockTTLSeconds: 60, RequestID: "req-1", TraceParent: "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01", LeaseOwner: "lease-1"})
	require.Equal(t, int32(2), locker.attempts.Load())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExecuteManualRecordsHandlerPanicAsFailure(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := &Manager{
		cfg:      config.Config{App: config.App{Name: "orders-service"}},
		tx:       database.NewTransactor(sqlx.NewDb(raw, "sqlmock")),
		locker:   lockerStub{mutex: mutexStub{}, acquired: true},
		metrics:  &observability.Metrics{},
		logger:   logger,
		handlers: NewHandlerRegistry(logger),
		operations: runLogRecorderStub{recordTx: func(_ context.Context, _ *sqlx.Tx, entry operationlog.Entry) error {
			require.Equal(t, "run-1", entry.ResourceID)
			require.False(t, entry.Succeeded)
			require.Equal(t, "job_error", entry.ErrorCode)
			return nil
		}},
	}
	manager.handlers.handlers["system.sample"] = HandlerDefinition{Key: "system.sample", handler: func(context.Context, json.RawMessage) error {
		panic("secret-value")
	}}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE scheduled_job_runs SET status=`).WithArgs("error", sqlmock.AnyArg(), sqlmock.AnyArg(), "scheduled job execution failed", sqlmock.AnyArg(), "orders-service:scheduler:manual", "run-1", "lease-1", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	manager.executeManual(t.Context(), manualRun{ID: "run-1", JobCode: "sample-job", JobName: "Sample", Handler: "system.sample", Payload: "{}", TimeoutSeconds: 30, LockTTLSeconds: 60, LeaseOwner: "lease-1"})
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExecuteManualCompletionFailureKeepsCorrelationAndErrorMetric(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	logHandler := &manualFailureLogHandler{}
	logger := slog.New(logHandler)
	metrics := observability.NewMetrics(config.Config{App: config.App{Name: "orders-service"}, Observability: config.Observability{MetricsEnabled: true}}, nil, nil)
	manager := &Manager{
		cfg:      config.Config{App: config.App{Name: "orders-service"}},
		tx:       database.NewTransactor(sqlx.NewDb(raw, "sqlmock")),
		locker:   lockerStub{mutex: mutexStub{}, acquired: true},
		metrics:  metrics,
		logger:   logger,
		handlers: NewHandlerRegistry(logger),
	}
	manager.handlers.handlers["system.sample"] = HandlerDefinition{Key: "system.sample", handler: func(context.Context, json.RawMessage) error { return nil }}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE scheduled_job_runs SET status=`).WillReturnError(errors.New("secret database detail"))
	mock.ExpectRollback()
	manager.executeManual(t.Context(), manualRun{ID: "run-1", JobCode: "sample-job", JobName: "Sample", Handler: "system.sample", Payload: "{}", TimeoutSeconds: 30, LockTTLSeconds: 60, RequestID: "req-1", TraceParent: "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01", LeaseOwner: "lease-1"})
	require.Equal(t, "finish manual scheduled job", logHandler.message)
	require.Equal(t, "req-1", logHandler.requestID)
	require.Equal(t, "0102030405060708090a0b0c0d0e0f10", logHandler.traceID)
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	require.Contains(t, response.Body.String(), `cron_runs_total{job="system.sample",status="error"} 1`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExecuteManualShutdownLeavesRunRecoverable(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	manager := &Manager{
		cfg:      config.Config{App: config.App{Name: "orders-service"}},
		tx:       database.NewTransactor(sqlx.NewDb(raw, "sqlmock")),
		locker:   lockerStub{mutex: mutexStub{}, acquired: true},
		metrics:  &observability.Metrics{},
		logger:   logger,
		handlers: NewHandlerRegistry(logger),
	}
	called := false
	manager.handlers.handlers["system.sample"] = HandlerDefinition{Key: "system.sample", handler: func(ctx context.Context, _ json.RawMessage) error {
		called = true
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}}
	manager.executeManual(parent, manualRun{ID: "run-1", JobCode: "sample-job", JobName: "Sample", Handler: "system.sample", Payload: "{}", TimeoutSeconds: 30, LockTTLSeconds: 60, LeaseOwner: "lease-1"})
	require.True(t, called)
	require.NotContains(t, output.String(), "finish manual scheduled job", "shutdown must leave the run eligible for lease recovery")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWatchManualLeaseIgnoresCancellationDuringRenewal(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			result <- watchManualLease(ctx, done, time.Second, func(context.Context) error {
				cancel()
				return context.Canceled
			})
		}()
		time.Sleep(time.Second)
		synctest.Wait()
		require.NoError(t, <-result, "a canceled task must not be reported as lease loss")
	})
}

func TestWatchManualLeaseReportsFailureWhileTaskIsActive(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		result := make(chan error, 1)
		failure := errors.New("database unavailable")
		go func() {
			result <- watchManualLease(ctx, done, time.Second, func(context.Context) error {
				return failure
			})
		}()
		time.Sleep(time.Second)
		synctest.Wait()
		require.ErrorIs(t, <-result, failure)
	})
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
