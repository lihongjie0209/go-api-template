package authorization

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestPlatformGrantValidation(t *testing.T) {
	t.Parallel()
	valid := PlatformGrantInput{PrincipalType: "user", PrincipalID: "18f4762f-f4f9-47d5-bd7a-6f07af436f58", PermissionID: "08896c3a-4c82-473e-90c8-b59f60446139"}
	require.NoError(t, validatePlatformGrant(valid))
	valid.PrincipalType = "anonymous"
	require.ErrorIs(t, validatePlatformGrant(valid), ErrPlatformGrantInvalid)
	valid.PrincipalType = "user"
	valid.PrincipalID = "*"
	require.ErrorIs(t, validatePlatformGrant(valid), ErrPlatformGrantInvalid)
}

func TestPlatformGrantRejectsUnknownPrincipalBeforeWriting(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	sqlxDB := sqlx.NewDb(db, "pgx")
	service := NewPlatformGrantService(sqlxDB, database.NewTransactor(sqlxDB), nil, nil)
	ctx := platformprincipal.SystemContext(t.Context(), "grant-admin")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.actor_id', $1, true)")).WithArgs("grant-admin").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM identity_users").WithArgs("18f4762f-f4f9-47d5-bd7a-6f07af436f58").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectRollback()
	_, err = service.Create(ctx, PlatformGrantInput{PrincipalType: "user", PrincipalID: "18f4762f-f4f9-47d5-bd7a-6f07af436f58", PermissionID: "08896c3a-4c82-473e-90c8-b59f60446139"})
	require.ErrorIs(t, err, ErrPlatformGrantInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}
