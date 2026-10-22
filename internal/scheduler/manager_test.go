package scheduler

import (
	"context"
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
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/require"
)

type runLogRecorderStub struct {
	recordTx func(context.Context, *sqlx.Tx, operationlog.Entry) error
}

func (runLogRecorderStub) Enabled() bool                                    { return true }
func (runLogRecorderStub) Record(context.Context, operationlog.Entry) error { return nil }
func (s runLogRecorderStub) RecordTx(ctx context.Context, tx *sqlx.Tx, entry operationlog.Entry) error {
	return s.recordTx(ctx, tx, entry)
}

func TestManagerRefreshAtomicallyReplacesValidatedDefinitions(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handlers := NewHandlerRegistry(logger)
	manager := &Manager{
		cfg:            config.Config{Database: config.Database{Enabled: true}},
		db:             db,
		handlers:       handlers,
		logger:         logger,
		runner:         cron.New(cron.WithSeconds()),
		dynamicEntries: map[string]cron.EntryID{},
	}
	now := time.Now()
	columns := []string{"id", "code", "name", "description", "cron_spec", "timezone", "handler", "timeout_seconds", "lock_ttl_seconds", "status", "payload", "created_at", "created_by", "updated_at", "updated_by", "version"}
	mock.ExpectQuery(`SELECT .* FROM scheduled_jobs`).WillReturnRows(sqlmock.NewRows(columns).AddRow("job-1", "sample-job", "Sample", "", "0 * * * * *", "Asia/Shanghai", "system.sample", 30, 60, "active", []byte(`{}`), now, "system", now, "system", 1))
	require.NoError(t, manager.Refresh(t.Context()))
	require.Len(t, manager.dynamicEntries, 1)
	require.Len(t, manager.runner.Entries(), 1)

	mock.ExpectQuery(`SELECT .* FROM scheduled_jobs`).WillReturnRows(sqlmock.NewRows(columns).AddRow("job-2", "unknown-job", "Unknown", "", "0 * * * * *", "Asia/Shanghai", "unknown.handler", 30, 60, "active", []byte(`{}`), now, "system", now, "system", 1))
	require.ErrorIs(t, manager.Refresh(t.Context()), ErrHandlerNotRegistered)
	require.Len(t, manager.dynamicEntries, 1, "invalid snapshots must not replace the active schedule")

	mock.ExpectQuery(`SELECT .* FROM scheduled_jobs`).WillReturnRows(sqlmock.NewRows(columns).AddRow("job-2", "timezone-job", "Timezone", "", "CRON_TZ=UTC 0 * * * * *", "Asia/Shanghai", "system.sample", 30, 60, "active", []byte(`{}`), now, "system", now, "system", 1))
	require.ErrorIs(t, manager.Refresh(t.Context()), ErrInvalidDefinition)
	require.Len(t, manager.dynamicEntries, 1, "an inline timezone must not remove the active schedule")
	require.Len(t, manager.runner.Entries(), 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagerExecuteRecordsBoundedHistory(t *testing.T) {
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
			require.Equal(t, "platform.scheduled-job.run.completed", entry.Operation)
			require.Equal(t, "scheduled_job_run", entry.ResourceType)
			require.NotEmpty(t, entry.ResourceID)
			require.Equal(t, "Sample", entry.ResourceName)
			require.True(t, entry.Succeeded)
			require.NotEmpty(t, mustRequestID(t, ctx))
			return nil
		}},
	}
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO scheduled_job_runs`).
		WithArgs(sqlmock.AnyArg(), "job-1", "sample-job", "Sample", "system.sample", "manual", "success", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "", sqlmock.AnyArg(), "orders-service:scheduler:sample-job", sqlmock.AnyArg(), "orders-service:scheduler:sample-job").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	err = manager.execute(requestid.WithContext(t.Context(), "req-1"), Definition{ID: "job-1", Code: "sample-job", Name: "Sample", Handler: "system.sample", TimeoutSeconds: 30, LockTTLSeconds: 60}, "manual")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagerExecuteRecordsMissingHandlerFailure(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := &Manager{
		cfg:      config.Config{App: config.App{Name: "orders-service"}},
		tx:       database.NewTransactor(db),
		metrics:  &observability.Metrics{},
		logger:   logger,
		handlers: NewHandlerRegistry(logger),
		operations: runLogRecorderStub{recordTx: func(_ context.Context, tx *sqlx.Tx, entry operationlog.Entry) error {
			require.NotNil(t, tx)
			require.Equal(t, "platform.scheduled-job.run.completed", entry.Operation)
			require.False(t, entry.Succeeded)
			require.Equal(t, "job_error", entry.ErrorCode)
			require.Equal(t, "scheduled job did not complete successfully", entry.ErrorMessage)
			return nil
		}},
	}
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO scheduled_job_runs`).
		WithArgs(sqlmock.AnyArg(), "job-1", "orphaned-job", "Orphaned", "removed.handler", "cron", "error", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "scheduled job execution failed", sqlmock.AnyArg(), "orders-service:scheduler:orphaned-job", sqlmock.AnyArg(), "orders-service:scheduler:orphaned-job").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	err = manager.execute(t.Context(), Definition{ID: "job-1", Code: "orphaned-job", Name: "Orphaned", Handler: "removed.handler", TimeoutSeconds: 30, LockTTLSeconds: 60}, "cron")
	require.ErrorIs(t, err, ErrHandlerNotRegistered)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagerExecuteRecordsHandlerPanicAsFailure(t *testing.T) {
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
			require.False(t, entry.Succeeded)
			require.Equal(t, "job_error", entry.ErrorCode)
			return nil
		}},
	}
	manager.handlers.handlers["system.sample"] = HandlerDefinition{Key: "system.sample", handler: func(context.Context, json.RawMessage) error {
		panic("secret-value")
	}}
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO scheduled_job_runs`).
		WithArgs(sqlmock.AnyArg(), "job-1", "sample-job", "Sample", "system.sample", "cron", "error", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "scheduled job execution failed", sqlmock.AnyArg(), "orders-service:scheduler:sample-job", sqlmock.AnyArg(), "orders-service:scheduler:sample-job").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	err = manager.execute(t.Context(), Definition{ID: "job-1", Code: "sample-job", Name: "Sample", Handler: "system.sample", TimeoutSeconds: 30, LockTTLSeconds: 60}, "cron")
	require.ErrorIs(t, err, ErrJobPanicked)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagerExecuteRecordsCancellationAfterHandlerReturnsSuccess(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	manager := &Manager{
		cfg:      config.Config{App: config.App{Name: "orders-service"}},
		tx:       database.NewTransactor(sqlx.NewDb(raw, "sqlmock")),
		locker:   lockerStub{mutex: mutexStub{}, acquired: true},
		metrics:  &observability.Metrics{},
		logger:   logger,
		handlers: NewHandlerRegistry(logger),
		operations: runLogRecorderStub{recordTx: func(_ context.Context, _ *sqlx.Tx, entry operationlog.Entry) error {
			require.False(t, entry.Succeeded)
			require.Equal(t, "job_error", entry.ErrorCode)
			return nil
		}},
	}
	manager.handlers.handlers["system.sample"] = HandlerDefinition{Key: "system.sample", handler: func(context.Context, json.RawMessage) error {
		cancel()
		return nil
	}}
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO scheduled_job_runs`).
		WithArgs(sqlmock.AnyArg(), "job-1", "sample-job", "Sample", "system.sample", "cron", "error", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "scheduled job execution failed", sqlmock.AnyArg(), "orders-service:scheduler:sample-job", sqlmock.AnyArg(), "orders-service:scheduler:sample-job").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	err = manager.execute(parent, Definition{ID: "job-1", Code: "sample-job", Name: "Sample", Handler: "system.sample", TimeoutSeconds: 30, LockTTLSeconds: 60}, "cron")
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagerRunRecordRollsBackWhenOperationLogCannotBeQueued(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := &Manager{
		cfg:    config.Config{App: config.App{Name: "orders-service"}},
		tx:     database.NewTransactor(db),
		logger: logger,
		operations: runLogRecorderStub{recordTx: func(context.Context, *sqlx.Tx, operationlog.Entry) error {
			return errors.New("outbox unavailable")
		}},
	}
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO scheduled_job_runs`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectRollback()
	ctx := requestid.WithContext(platformprincipal.SystemContext(t.Context(), "orders-service:scheduler:sample-job"), "req-1")
	err = manager.recordRun(ctx, Definition{ID: "job-1", Code: "sample-job", Name: "Sample", Handler: "system.sample"}, "cron", "success", time.Now(), time.Now())
	require.ErrorContains(t, err, "outbox unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func mustRequestID(t *testing.T, ctx context.Context) string {
	t.Helper()
	id, ok := requestid.FromContext(ctx)
	require.True(t, ok)
	return id
}
