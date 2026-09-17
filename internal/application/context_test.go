package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func applicationContextTestPrincipal(ctx context.Context) context.Context {
	return platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1", TenantID: "tenant-1", MembershipID: "membership-1"})
}

func TestCurrentApplicationContextScopesEveryPrincipalDimension(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(`FROM application_session_contexts c JOIN identity_sessions s.*JOIN tenant_memberships m.*JOIN tenant_application_grants g.*WHERE c.session_id=\? AND c.tenant_id=\? AND c.membership_id=\? AND c.user_id=\?`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "session-1", "tenant-1", "membership-1", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{}))
	_, err = (&TenantAccessService{db: db}).CurrentContext(applicationContextTestPrincipal(t.Context()))
	require.ErrorIs(t, err, ErrContextNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSwitchApplicationContextCreatesThenReturnsAuthorizedContext(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM applications a JOIN tenant_application_grants g.*JOIN identity_sessions s`).
		WithArgs("tenant-1", sqlmock.AnyArg(), sqlmock.AnyArg(), "membership-1", "user-1", "session-1", sqlmock.AnyArg(), "app-1").
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Console"))
	mock.ExpectQuery(`SELECT id,version FROM application_session_contexts`).WithArgs("session-1", "tenant-1").WillReturnRows(sqlmock.NewRows([]string{"id", "version"}))
	mock.ExpectExec(`INSERT INTO application_session_contexts`).WithArgs(sqlmock.AnyArg(), "session-1", "user-1", "tenant-1", "membership-1", "app-1", sqlmock.AnyArg(), "user-1", sqlmock.AnyArg(), "user-1").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	now := time.Now()
	mock.ExpectQuery(`FROM application_session_contexts c JOIN identity_sessions s`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "session-1", "tenant-1", "membership-1", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "application_id", "application_code", "application_name", "icon", "home_path", "created_at", "created_by", "updated_at", "updated_by", "version"}).AddRow("context-1", "app-1", "console", "Console", "", "/", now, "user-1", now, "user-1", 1))
	service := &TenantAccessService{db: db, tx: database.NewTransactor(db)}
	result, err := service.SwitchContext(applicationContextTestPrincipal(t.Context()), SwitchInput{ApplicationID: "app-1", Version: 0})
	require.NoError(t, err)
	require.Equal(t, "app-1", result.ApplicationID)
	require.Equal(t, int64(1), result.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSwitchApplicationContextRejectsMissingSession(t *testing.T) {
	t.Parallel()
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1"})
	_, err := (&TenantAccessService{}).SwitchContext(ctx, SwitchInput{ApplicationID: "app-1"})
	require.ErrorIs(t, err, ErrGrantInvalid)
}

func TestSwitchApplicationContextRollsBackInsertFailure(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM applications a JOIN tenant_application_grants g`).
		WithArgs("tenant-1", sqlmock.AnyArg(), sqlmock.AnyArg(), "membership-1", "user-1", "session-1", sqlmock.AnyArg(), "app-1").
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Console"))
	mock.ExpectQuery(`SELECT id,version FROM application_session_contexts`).WithArgs("session-1", "tenant-1").WillReturnRows(sqlmock.NewRows([]string{"id", "version"}))
	mock.ExpectExec(`INSERT INTO application_session_contexts`).WillReturnError(errors.New("write unavailable"))
	mock.ExpectRollback()
	service := &TenantAccessService{db: db, tx: database.NewTransactor(db)}
	_, err = service.SwitchContext(applicationContextTestPrincipal(t.Context()), SwitchInput{ApplicationID: "app-1"})
	require.ErrorContains(t, err, "write unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}
