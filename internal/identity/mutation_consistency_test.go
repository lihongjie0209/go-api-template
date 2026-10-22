package identity

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestCreateRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &Service{repository: NewRepository(db), transactor: database.NewTransactor(db), operations: operationStub{}, security: securityStub{}}
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO identity_users`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM identity_users u WHERE id = \? AND deleted_at IS NULL`).
		WithArgs(sqlmock.AnyArg()).WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	_, err = service.Create(platformprincipal.SystemContext(t.Context(), "admin"), CreateInput{Username: "alice", DisplayName: "Alice"})
	require.ErrorContains(t, err, "result read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &Service{repository: NewRepository(db), transactor: database.NewTransactor(db), operations: operationStub{}, security: securityStub{}}
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM identity_users u WHERE id = \? AND deleted_at IS NULL`).
		WithArgs("user-1").WillReturnRows(sqlmock.NewRows(userColumnNames()).AddRow(userRow("user-1", StatusActive, 2, now)...))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE identity_users SET display_name=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM identity_users u WHERE id = \? AND deleted_at IS NULL`).
		WithArgs("user-1").WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	_, err = service.Update(platformprincipal.SystemContext(t.Context(), "admin"), UpdateInput{ID: "user-1", DisplayName: "Alice Updated", Status: StatusActive, Version: 2})
	require.ErrorContains(t, err, "result read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateSelfRollsBackWithoutInvalidatingCacheWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	store := &memoryStore{values: map[string][]byte{}}
	service := &Service{repository: NewRepository(db), transactor: database.NewTransactor(db), cache: store, operations: operationStub{}, security: securityStub{}}
	old := User{ID: "user-self", Username: "alice", DisplayName: "Alice", Status: StatusActive, Version: 2}
	service.cacheUser(t.Context(), old)
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM identity_users u WHERE id = \? AND deleted_at IS NULL`).
		WithArgs("user-self").WillReturnRows(sqlmock.NewRows(userColumnNames()).AddRow(userRow("user-self", StatusActive, 2, now)...))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE identity_users SET display_name=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM identity_users u WHERE id = \? AND deleted_at IS NULL`).
		WithArgs("user-self").WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-self", Type: platformprincipal.TypeUser})
	_, err = service.UpdateSelf(ctx, SelfUpdateInput{DisplayName: "Alice Updated", Version: 2})
	require.ErrorContains(t, err, "result read unavailable")
	value, err := cache.GetJSON[User](t.Context(), store, "identity:user:v1:id:user-self")
	require.NoError(t, err)
	require.Equal(t, old.Version, value.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateRejectsClosedStatusWithoutDatabaseWrite(t *testing.T) {
	t.Parallel()
	service := &Service{}
	_, err := service.Update(platformprincipal.SystemContext(t.Context(), "admin"), UpdateInput{ID: "user-1", DisplayName: "Alice", Status: StatusClosed, Version: 2})
	require.ErrorIs(t, err, ErrInvalid)
}

func TestClosedUserCannotBeReactivatedOrSelfEdited(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(context.Context, *Service) error
	}{
		{name: "administrator update", mutate: func(ctx context.Context, service *Service) error {
			_, err := service.Update(ctx, UpdateInput{ID: "user-1", DisplayName: "Alice", Status: StatusActive, Version: 2})
			return err
		}},
		{name: "self update", mutate: func(ctx context.Context, service *Service) error {
			_, err := service.UpdateSelf(ctx, SelfUpdateInput{DisplayName: "Alice", Version: 2})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			service := &Service{repository: NewRepository(sqlx.NewDb(raw, "sqlmock"))}
			mock.ExpectQuery(`SELECT .* FROM identity_users u WHERE id = \? AND deleted_at IS NULL`).
				WithArgs("user-1").WillReturnRows(sqlmock.NewRows(userColumnNames()).AddRow(userRow("user-1", StatusClosed, 2, time.Now())...))
			ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
			require.ErrorIs(t, test.mutate(ctx, service), ErrNotFound)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUserMutationOperationLogOmitsContacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Service, sqlmock.Sqlmock) error
	}{
		{name: "create", mutate: func(service *Service, mock sqlmock.Sqlmock) error {
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO identity_users`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(`SELECT .* FROM identity_users u WHERE id = \? AND deleted_at IS NULL`).
				WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(userColumnNames()).AddRow(userRow("user-1", StatusActive, 1, time.Now())...))
			mock.ExpectCommit()
			_, err := service.Create(platformprincipal.SystemContext(t.Context(), "admin"), CreateInput{Username: "alice", DisplayName: "Alice", Email: "alice@example.com", Phone: "13800000000"})
			return err
		}},
		{name: "update", mutate: func(service *Service, mock sqlmock.Sqlmock) error {
			mock.ExpectQuery(`SELECT .* FROM identity_users u WHERE id = \? AND deleted_at IS NULL`).
				WithArgs("user-1").WillReturnRows(sqlmock.NewRows(userColumnNames()).AddRow(userRow("user-1", StatusActive, 1, time.Now())...))
			mock.ExpectBegin()
			mock.ExpectExec(`UPDATE identity_users SET display_name=`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(`SELECT .* FROM identity_users u WHERE id = \? AND deleted_at IS NULL`).
				WithArgs("user-1").WillReturnRows(sqlmock.NewRows(userColumnNames()).AddRow(userRow("user-1", StatusActive, 2, time.Now())...))
			mock.ExpectCommit()
			_, err := service.Update(platformprincipal.SystemContext(t.Context(), "admin"), UpdateInput{ID: "user-1", DisplayName: "Alice", Email: "alice@example.com", Phone: "13800000000", Status: StatusActive, Version: 1})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			operations := &capturingOperationRecorder{}
			service := &Service{repository: NewRepository(db), transactor: database.NewTransactor(db), operations: operations, security: securityStub{}}
			require.NoError(t, test.mutate(service, mock))
			require.Len(t, operations.entries, 1)
			encoded, err := json.Marshal(operations.entries[0].Request)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "alice@example.com")
			require.NotContains(t, string(encoded), "13800000000")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
