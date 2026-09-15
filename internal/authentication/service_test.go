package authentication

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestIdentitySessionInsertInitializesRequiredState(t *testing.T) {
	t.Parallel()
	for _, column := range []string{"previous_refresh_token_hash", "revoke_reason"} {
		if !strings.Contains(identitySessionInsertSQL, column) {
			t.Fatalf("session insert does not initialize required column %q", column)
		}
	}
}

func TestService_ChangePasswordRevokesEveryActiveSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	hasher := auth.NewPasswordHasher()
	oldHash, err := hasher.Hash("old password value")
	require.NoError(t, err)
	service := New(sqlxDB, database.NewTransactor(sqlxDB), nil, nil, config.Config{})
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id,user_id,password_hash`).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "password_hash", "failed_attempts", "locked_until", "version"}).AddRow("credential-1", "user-1", oldHash, 0, nil, 3))
	mock.ExpectExec(`UPDATE identity_user_credentials`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "user-1", "credential-1", int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_sessions`).WithArgs(sqlmock.AnyArg(), "password_changed", sqlmock.AnyArg(), "user-1", "user-1").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	require.NoError(t, service.ChangePassword(ctx, "old password value", "new password value"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestService_SessionsAlwaysScopesQueryToCurrentUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	service := New(sqlxDB, database.NewTransactor(sqlxDB), nil, nil, config.Config{})
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	mock.ExpectQuery(`SELECT count\(\*\) FROM identity_sessions WHERE user_id=`).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT id,user_id,expires_at`).WithArgs("user-1", 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "expires_at", "last_seen_at", "revoked_at", "revoke_reason", "client_ip", "user_agent", "created_at", "version"}))
	page, err := service.Sessions(ctx, 1, 20)
	require.NoError(t, err)
	require.Zero(t, page.Total)
	require.NotNil(t, page.Items)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestService_RecordFailureUsesAtomicIncrement(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	service := New(sqlxDB, database.NewTransactor(sqlxDB), nil, nil, config.Config{Authentication: config.Authentication{MaxFailedAttempts: 5, LockDuration: 15 * time.Minute}})
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE identity_user_credentials SET failed_attempts=failed_attempts\+1,locked_until=CASE`).WithArgs(int64(5), sqlmock.AnyArg(), sqlmock.AnyArg(), "user-1", "credential-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, service.recordFailure(t.Context(), Credential{ID: "credential-1", UserID: "user-1", FailedAttempts: 99, Version: 8}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestService_SetPasswordRejectsUnknownUserBeforeCredentialInsert(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	service := New(sqlxDB, database.NewTransactor(sqlxDB), nil, nil, config.Config{})
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM identity_users`).WithArgs("missing-user").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectRollback()
	require.ErrorIs(t, service.SetPassword(ctx, "missing-user", "correct horse battery staple"), identity.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
