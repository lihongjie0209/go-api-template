package scheduler

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/require"
)

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
	}
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO scheduled_job_runs`).
		WithArgs(sqlmock.AnyArg(), "job-1", "sample-job", "Sample", "system.sample", "manual", "success", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "", sqlmock.AnyArg(), "orders-service:scheduler:sample-job", sqlmock.AnyArg(), "orders-service:scheduler:sample-job").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	err = manager.execute(t.Context(), Definition{ID: "job-1", Code: "sample-job", Name: "Sample", Handler: "system.sample", TimeoutSeconds: 30, LockTTLSeconds: 60}, "manual")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
