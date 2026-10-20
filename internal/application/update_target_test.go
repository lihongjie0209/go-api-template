package application

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestUpdateTargetUsesEditableAuthoritativeQuery(t *testing.T) {
	t.Parallel()
	id := "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	query := `SELECT id,code,name,description,icon,home_path,status,sort_order,metadata,version FROM applications WHERE id=\? AND deleted_at IS NULL`
	mock.ExpectQuery(query).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{
		"id", "code", "name", "description", "icon", "home_path", "status", "sort_order", "metadata", "version",
	}).AddRow(id, "erp", "ERP", "description", "icon", "/erp", "active", int64(2), []byte(`{"x":1}`), int64(4)))
	service := New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
	target, err := service.UpdateTarget(platformprincipal.SystemContext(t.Context(), "admin"), id)
	require.NoError(t, err)
	require.Equal(t, UpdateTarget{
		ID: id, Code: "erp", Name: "ERP", Description: "description", Icon: "icon",
		HomePath: "/erp", Status: "active", SortOrder: 2, Metadata: []byte(`{"x":1}`), Version: 4,
	}, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateTargetRejectsMissingPrincipalInvalidIDAndMissingRow(t *testing.T) {
	t.Parallel()
	id := "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
	_, err = service.UpdateTarget(t.Context(), id)
	require.Error(t, err)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	for _, invalid := range []string{"", "not-a-uuid", "8B74CC0B-EA4E-4C3C-8CB2-8F9D83B75FA0"} {
		_, err = service.UpdateTarget(ctx, invalid)
		require.ErrorIs(t, err, ErrInvalid)
	}
	query := `SELECT id,code,name,description,icon,home_path,status,sort_order,metadata,version FROM applications WHERE id=\? AND deleted_at IS NULL`
	mock.ExpectQuery(query).WithArgs(id).WillReturnError(sql.ErrNoRows)
	_, err = service.UpdateTarget(ctx, id)
	require.ErrorIs(t, err, ErrNotFound)
	databaseFailure := errors.New("database unavailable")
	mock.ExpectQuery(query).WithArgs(id).WillReturnError(databaseFailure)
	_, err = service.UpdateTarget(ctx, id)
	require.ErrorIs(t, err, databaseFailure)
	require.NoError(t, mock.ExpectationsWereMet())
}
