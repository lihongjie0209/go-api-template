package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type unavailableDefinitionActors struct{}

func (unavailableDefinitionActors) ResolveUserIDs(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("identity unavailable")
}

func definitionRows(id string, version int64) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows([]string{"id", "code", "name", "description", "cron_spec", "timezone", "handler", "timeout_seconds", "lock_ttl_seconds", "status", "payload", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow(id, "report", "Report", "", "0 * * * * *", "Asia/Shanghai", "system.sample", 60, 120, "active", []byte(`{}`), now, "creator", now, "updater", version)
}

func TestDefinitionMutationsSurviveActorResolverOutage(t *testing.T) {
	t.Parallel()
	id := "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			service := NewDefinitionService(db, database.NewTransactor(db), nil, unavailableDefinitionActors{}, NewHandlerRegistry(slog.Default()), &Manager{}, nil)
			mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnRows(definitionRows(id, 4))
			mock.ExpectBegin()
			if operation == "update" {
				mock.ExpectExec(`UPDATE scheduled_jobs SET .* WHERE id=\? AND version=\? AND deleted_at IS NULL`).
					WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), id, int64(4)).
					WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnRows(definitionRows(id, 5))
			} else {
				mock.ExpectExec(`UPDATE scheduled_jobs SET .* WHERE id=\? AND version=\? AND deleted_at IS NULL`).
					WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), id, int64(4)).
					WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()
			ctx := platformprincipal.SystemContext(t.Context(), "admin")
			if operation == "update" {
				updated, err := service.Update(ctx, UpdateDefinitionInput{ID: id, Name: "Report", CronSpec: "0 * * * * *", Timezone: "Asia/Shanghai", Handler: "system.sample", TimeoutSeconds: 60, LockTTLSeconds: 120, Status: "active", Payload: []byte(`{}`), Version: 4})
				require.NoError(t, err)
				require.EqualValues(t, 5, updated.Version)
				require.Equal(t, "creator", updated.CreatedByName)
				require.Equal(t, "updater", updated.UpdatedByName)
			} else {
				require.NoError(t, service.Delete(ctx, id, 4))
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDefinitionCreateReturnsCommittedRowWithoutActorResolver(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := NewDefinitionService(db, database.NewTransactor(db), nil, unavailableDefinitionActors{}, NewHandlerRegistry(slog.Default()), &Manager{}, nil)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO scheduled_jobs`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=\? AND deleted_at IS NULL`).WithArgs(sqlmock.AnyArg()).WillReturnRows(definitionRows("8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0", 1))
	mock.ExpectCommit()
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	created, err := service.Create(ctx, DefinitionInput{Code: "report", Name: "Report", CronSpec: "0 * * * * *", Timezone: "Asia/Shanghai", Handler: "system.sample", TimeoutSeconds: 60, LockTTLSeconds: 120, Status: "active", Payload: []byte(`{}`)})
	require.NoError(t, err)
	require.EqualValues(t, 1, created.Version)
	require.Equal(t, "creator", created.CreatedByName)
	require.Equal(t, "updater", created.UpdatedByName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDefinitionUpdateRollsBackWhenCommittedRowCannotBeLoaded(t *testing.T) {
	t.Parallel()
	id := "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := NewDefinitionService(db, database.NewTransactor(db), nil, nil, NewHandlerRegistry(slog.Default()), &Manager{}, nil)
	mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnRows(definitionRows(id, 4))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE scheduled_jobs SET .* WHERE id=\? AND version=\? AND deleted_at IS NULL`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), id, int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	loadFailure := errors.New("transactional read failed")
	mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnError(loadFailure)
	mock.ExpectRollback()
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	_, err = service.Update(ctx, UpdateDefinitionInput{ID: id, Name: "Report", CronSpec: "0 * * * * *", Timezone: "Asia/Shanghai", Handler: "system.sample", TimeoutSeconds: 60, LockTTLSeconds: 120, Status: "active", Payload: []byte(`{}`), Version: 4})
	require.ErrorIs(t, err, loadFailure)
	require.NoError(t, mock.ExpectationsWereMet())
}
