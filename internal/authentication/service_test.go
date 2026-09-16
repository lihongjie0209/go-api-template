package authentication

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type transactionalSecurityRecorder struct {
	err     error
	entries []securitylog.Entry
}

func (*transactionalSecurityRecorder) Enabled() bool    { return true }
func (*transactionalSecurityRecorder) FailClosed() bool { return true }
func (r *transactionalSecurityRecorder) RecordTx(_ context.Context, _ *sqlx.Tx, entry securitylog.Entry) error {
	r.entries = append(r.entries, entry)
	return r.err
}

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
	require.NoError(t, service.recordFailure(t.Context(), Credential{ID: "credential-1", UserID: "user-1", FailedAttempts: 99, Version: 8}, "alice", "127.0.0.1", "test"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestService_RecordFailureRollsBackWhenSecurityEventCannotBeStored(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	recorder := &transactionalSecurityRecorder{err: errors.New("outbox unavailable")}
	service := NewWithSecurity(sqlxDB, database.NewTransactor(sqlxDB), nil, nil, config.Config{Authentication: config.Authentication{MaxFailedAttempts: 5, LockDuration: 15 * time.Minute}}, recorder)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE identity_user_credentials SET failed_attempts=failed_attempts\+1,locked_until=CASE`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	err = service.recordFailure(t.Context(), Credential{ID: "credential-1", UserID: "user-1"}, "alice", "127.0.0.1", "test")
	require.ErrorIs(t, err, ErrSecurityUnavailable)
	require.Len(t, recorder.entries, 1)
	require.False(t, recorder.entries[0].Succeeded)
	require.Equal(t, "invalid_credentials", recorder.entries[0].Reason)
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

func TestService_SetPasswordClassifiesWeakPasswordAsInvalidInput(t *testing.T) {
	service := New(nil, &database.Transactor{}, nil, nil, config.Config{})
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	require.ErrorIs(t, service.SetPassword(ctx, "user-1", "short"), ErrInvalid)
}

func TestService_RefreshRollsBackRotationWhenAccessTokenSigningFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	service := New(sqlxDB, database.NewTransactor(sqlxDB), nil, auth.New(config.Config{}), config.Config{JWT: config.JWT{TTL: time.Hour}})
	raw := "refresh-token"
	hash := tokenHash(raw)
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s.id,s.user_id,s.refresh_token_hash`).WithArgs(hash, hash).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "refresh_token_hash", "previous_refresh_token_hash", "expires_at", "revoked_at", "version"}).
			AddRow("session-1", "user-1", hash, "", now.Add(time.Hour), nil, 2))
	mock.ExpectExec(`UPDATE identity_sessions SET previous_refresh_token_hash`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "user-1", "session-1", int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()
	_, err = service.Refresh(t.Context(), raw)
	require.ErrorContains(t, err, "signing is not configured")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestService_ChangePasswordRollsBackWhenSecurityEventCannotBeStored(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	hasher := auth.NewPasswordHasher()
	oldHash, err := hasher.Hash("old password value")
	require.NoError(t, err)
	recorder := &transactionalSecurityRecorder{err: errors.New("outbox unavailable")}
	service := NewWithSecurity(sqlxDB, database.NewTransactor(sqlxDB), nil, nil, config.Config{}, recorder)
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id,user_id,password_hash`).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "password_hash", "failed_attempts", "locked_until", "version"}).AddRow("credential-1", "user-1", oldHash, 0, nil, 3))
	mock.ExpectExec(`UPDATE identity_user_credentials`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_sessions`).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectRollback()

	err = service.ChangePassword(ctx, "old password value", "new password value")
	require.ErrorIs(t, err, ErrSecurityUnavailable)
	require.Len(t, recorder.entries, 1)
	require.Equal(t, securitylog.EventPasswordChanged, recorder.entries[0].EventType)
	require.NoError(t, mock.ExpectationsWereMet())
}
