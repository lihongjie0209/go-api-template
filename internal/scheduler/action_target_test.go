package scheduler

import (
	"database/sql"
	"encoding/json"
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

func TestDefinitionActionTargetsProjectOnlyRequiredFields(t *testing.T) {
	t.Parallel()
	id := "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := NewDefinitionService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, nil)
	columns := []string{"id", "code", "name", "description", "cron_spec", "timezone", "handler", "timeout_seconds", "lock_ttl_seconds", "status", "payload", "created_at", "created_by", "updated_at", "updated_by", "version"}
	now := time.Now()
	for range 2 {
		mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).
			WillReturnRows(sqlmock.NewRows(columns).AddRow(id, "report", "Report", "desc", "0 * * * * *", "Asia/Shanghai", "report.run", 60, 120, "active", []byte(`{"a":1}`), now, "creator", now, "updater", 4))
	}
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	update, err := service.UpdateTarget(ctx, id)
	require.NoError(t, err)
	require.Equal(t, UpdateTarget{ID: id, Code: "report", Name: "Report", Description: "desc", CronSpec: "0 * * * * *", Timezone: "Asia/Shanghai", Handler: "report.run", TimeoutSeconds: 60, LockTTLSeconds: 120, Status: "active", Payload: json.RawMessage(`{"a":1}`), Version: 4}, update)
	deletion, err := service.DeleteTarget(ctx, id)
	require.NoError(t, err)
	require.Equal(t, DeleteTarget{ID: id, Code: "report", Name: "Report", Status: "active", Version: 4}, deletion)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDefinitionActionTargetsRejectInvalidAndMissingRows(t *testing.T) {
	t.Parallel()
	id := "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := NewDefinitionService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, nil)
	_, err = service.UpdateTarget(t.Context(), id)
	require.Error(t, err)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	for _, invalid := range []string{"", "not-a-uuid", "8B74CC0B-EA4E-4C3C-8CB2-8F9D83B75FA0"} {
		_, err = service.UpdateTarget(ctx, invalid)
		require.ErrorIs(t, err, ErrInvalidDefinition)
		_, err = service.DeleteTarget(ctx, invalid)
		require.ErrorIs(t, err, ErrInvalidDefinition)
	}
	for range 2 {
		mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnError(sql.ErrNoRows)
	}
	_, err = service.UpdateTarget(ctx, id)
	require.ErrorIs(t, err, ErrDefinitionNotFound)
	_, err = service.DeleteTarget(ctx, id)
	require.ErrorIs(t, err, ErrDefinitionNotFound)
	dbFailure := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnError(dbFailure)
	_, err = service.UpdateTarget(ctx, id)
	require.ErrorIs(t, err, dbFailure)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDefinitionVersionedMutationsRejectStaleRows(t *testing.T) {
	t.Parallel()
	id := "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			service := NewDefinitionService(db, database.NewTransactor(db), nil, nil, NewHandlerRegistry(slog.Default()), nil, nil)
			columns := []string{"id", "code", "name", "description", "cron_spec", "timezone", "handler", "timeout_seconds", "lock_ttl_seconds", "status", "payload", "created_at", "created_by", "updated_at", "updated_by", "version"}
			now := time.Now()
			mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).
				WillReturnRows(sqlmock.NewRows(columns).AddRow(id, "report", "Report", "", "0 * * * * *", "Asia/Shanghai", "system.sample", 60, 120, "active", []byte(`{}`), now, "creator", now, "updater", 5))
			mock.ExpectBegin()
			if operation == "update" {
				mock.ExpectExec(`UPDATE scheduled_jobs SET .* WHERE id=\? AND version=\? AND deleted_at IS NULL`).
					WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), id, int64(4)).
					WillReturnResult(sqlmock.NewResult(0, 0))
			} else {
				mock.ExpectExec(`UPDATE scheduled_jobs SET .* WHERE id=\? AND version=\? AND deleted_at IS NULL`).
					WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), id, int64(4)).
					WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectRollback()
			ctx := platformprincipal.SystemContext(t.Context(), "admin")
			if operation == "update" {
				_, err = service.Update(ctx, UpdateDefinitionInput{ID: id, Name: "Report", CronSpec: "0 * * * * *", Timezone: "Asia/Shanghai", Handler: "system.sample", TimeoutSeconds: 60, LockTTLSeconds: 120, Status: "active", Payload: []byte(`{}`), Version: 4})
			} else {
				err = service.Delete(ctx, id, 4)
			}
			require.ErrorIs(t, err, ErrDefinitionStaleVersion)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
