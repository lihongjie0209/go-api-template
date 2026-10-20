package identity

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestUpdateTargetReadsFreshEditableFieldsWithoutAuditOrCredentials(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	id := "d5298761-7e87-4393-b660-8d0c939a570b"
	query := `SELECT id,username,display_name,email,phone,status,version FROM identity_users WHERE id=\? AND deleted_at IS NULL`
	mock.ExpectQuery(query).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "email", "phone", "status", "version"}).AddRow(id, "alice", "Alice", "alice@example.com", "13800000000", "active", 4))
	service := &Service{repository: NewRepository(sqlx.NewDb(raw, "sqlmock"))}
	target, err := service.UpdateTarget(platformprincipal.SystemContext(t.Context(), "tester"), id)
	require.NoError(t, err)
	require.Equal(t, UpdateTarget{ID: id, Username: "alice", DisplayName: "Alice", Email: "alice@example.com", Phone: "13800000000", Status: StatusActive, Version: 4}, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateTargetRejectsInvalidContextAndPropagatesQueryFailure(t *testing.T) {
	t.Parallel()
	service := &Service{}
	id := "d5298761-7e87-4393-b660-8d0c939a570b"
	_, err := service.UpdateTarget(t.Context(), id)
	require.ErrorIs(t, err, platformprincipal.ErrMissing)
	ctx := platformprincipal.SystemContext(t.Context(), "tester")
	for _, invalid := range []string{"", "not-uuid", "D5298761-7E87-4393-B660-8D0C939A570B"} {
		_, err = service.UpdateTarget(ctx, invalid)
		require.ErrorIs(t, err, ErrInvalid)
	}
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service.repository = NewRepository(sqlx.NewDb(raw, "sqlmock"))
	query := `SELECT id,username,display_name,email,phone,status,version FROM identity_users WHERE id=\? AND deleted_at IS NULL`
	mock.ExpectQuery(query).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "email", "phone", "status", "version"}))
	_, err = service.UpdateTarget(ctx, id)
	require.ErrorIs(t, err, ErrNotFound)
	backendFailure := errors.New("database unavailable")
	mock.ExpectQuery(query).WithArgs(id).WillReturnError(backendFailure)
	_, err = service.UpdateTarget(ctx, id)
	require.ErrorIs(t, err, backendFailure)
	require.NoError(t, mock.ExpectationsWereMet())
}
