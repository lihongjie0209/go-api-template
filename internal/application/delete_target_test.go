package application

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestDeleteTargetUsesMinimalAuthoritativeQuery(t *testing.T) {
	t.Parallel()
	id := "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT id,code,name,status,version FROM applications WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "status", "version"}).AddRow(id, "erp", "ERP", "disabled", 4))
	service := New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
	target, err := service.DeleteTarget(platformprincipal.SystemContext(t.Context(), "admin"), id)
	require.NoError(t, err)
	require.Equal(t, DeleteTarget{ID: id, Code: "erp", Name: "ERP", Status: "disabled", Version: 4}, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteTargetValidatesPrincipalIDAndMissingRecord(t *testing.T) {
	t.Parallel()
	id := "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
	_, err = service.DeleteTarget(t.Context(), id)
	require.Error(t, err)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	for _, invalid := range []string{"", "not-a-uuid", "8B74CC0B-EA4E-4C3C-8CB2-8F9D83B75FA0"} {
		_, err = service.DeleteTarget(ctx, invalid)
		require.ErrorIs(t, err, ErrInvalid)
	}
	query := `SELECT id,code,name,status,version FROM applications WHERE id=\? AND deleted_at IS NULL`
	mock.ExpectQuery(query).WithArgs(id).WillReturnError(sql.ErrNoRows)
	_, err = service.DeleteTarget(ctx, id)
	require.ErrorIs(t, err, ErrNotFound)
	databaseFailure := errors.New("database unavailable")
	mock.ExpectQuery(query).WithArgs(id).WillReturnError(databaseFailure)
	_, err = service.DeleteTarget(ctx, id)
	require.ErrorIs(t, err, databaseFailure)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteDistinguishesStaleVersionFromNavigationConflict(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		children int
		want     error
	}{
		{name: "stale version", want: ErrStaleVersion},
		{name: "navigation still exists", children: 1, want: ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			service := New(db, database.NewTransactor(db), nil, nil)
			now := time.Now()
			mock.ExpectQuery(`SELECT .* FROM applications WHERE id=\? AND deleted_at IS NULL`).WithArgs("app-1").WillReturnRows(
				sqlmock.NewRows([]string{"id", "code", "name", "description", "icon", "home_path", "status", "sort_order", "metadata", "created_at", "created_by", "updated_at", "updated_by", "version"}).
					AddRow("app-1", "erp", "ERP", "", "", "/erp", "active", 0, []byte(`{}`), now, "admin", now, "admin", 4),
			)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT count\(\*\) FROM navigations WHERE application_id=\?`).WithArgs("app-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(test.children))
			if test.children == 0 {
				mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_application_grants WHERE application_id=\?`).WithArgs("app-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
				mock.ExpectExec(`UPDATE applications SET deleted_at=`).WithArgs(sqlmock.AnyArg(), "admin", sqlmock.AnyArg(), "admin", "app-1", int64(3)).WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectRollback()
			err = service.Delete(platformprincipal.SystemContext(t.Context(), "admin"), "app-1", 3)
			require.ErrorIs(t, err, test.want)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
