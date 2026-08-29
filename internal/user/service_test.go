package user

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/jmoiron/sqlx"
)

type fakeRepository struct {
	created User
	err     error
	users   []User
	total   int64
}

func (r *fakeRepository) Create(_ context.Context, _ sqlx.ExtContext, value User) error {
	r.created = value
	return r.err
}
func (*fakeRepository) Get(context.Context, string) (User, error) { return User{}, ErrNotFound }
func (r *fakeRepository) List(context.Context, int, int) ([]User, int64, error) {
	return r.users, r.total, r.err
}
func (*fakeRepository) Update(context.Context, User) error          { return nil }
func (*fakeRepository) Delete(context.Context, string, int64) error { return nil }

func TestService_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		repository *fakeRepository
		rollback   bool
		wantCode   int
	}{
		{name: "creates normalized user", repository: &fakeRepository{}},
		{name: "maps duplicate email", repository: &fakeRepository{err: ErrConflict}, rollback: true, wantCode: apperror.CodeConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectBegin()
			if test.rollback {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			service := newTestService(test.repository, database.NewTransactor(sqlx.NewDb(db, "sqlmock")))
			created, createErr := service.Create(t.Context(), " Alice ", "ALICE@EXAMPLE.COM")
			if test.wantCode == 0 {
				if createErr != nil {
					t.Fatalf("Create() error = %v", createErr)
				}
				if created.Name != "Alice" || created.Email != "alice@example.com" || created.Version != 1 {
					t.Fatalf("Create() = %+v", created)
				}
			} else {
				appErr, ok := createErr.(*apperror.Error)
				if !ok || appErr.Code != test.wantCode {
					t.Fatalf("Create() error = %v, want code %d", createErr, test.wantCode)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestService_ListValidation(t *testing.T) {
	t.Parallel()
	service := newTestService(&fakeRepository{}, availableTransactor(t))
	for _, test := range []struct {
		name     string
		page     int
		pageSize int
		wantErr  bool
	}{
		{name: "defaults", page: 0, pageSize: 0},
		{name: "maximum", page: 1, pageSize: 100},
		{name: "negative page", page: -1, pageSize: 20, wantErr: true},
		{name: "oversized page", page: 1, pageSize: 101, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := service.List(t.Context(), test.page, test.pageSize)
			if (err != nil) != test.wantErr {
				t.Fatalf("List() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func newTestService(repository Repository, transactor *database.Transactor) *Service {
	return NewService(repository, transactor, nil, nil, nil, config.Config{User: config.User{CacheTTL: time.Minute, LockTTL: time.Second, LockRetryDelay: time.Millisecond}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func availableTransactor(t *testing.T) *database.Transactor {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return database.NewTransactor(sqlx.NewDb(db, "sqlmock"))
}
