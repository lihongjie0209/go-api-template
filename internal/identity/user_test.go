package identity

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

func TestUsernameNormalization(t *testing.T) {
	t.Parallel()
	if got := normalizeUsername("  Alice.Smith "); got != "alice.smith" {
		t.Fatalf("normalizeUsername()=%q", got)
	}
}

func TestUserCacheMaintainsIDAndUsernameKeys(t *testing.T) {
	t.Parallel()
	store := &memoryStore{values: map[string][]byte{}}
	service := &Service{cache: store, cacheTTL: time.Minute, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	user := User{ID: "user-1", Username: "alice", DisplayName: "Alice", Version: 2}
	service.cacheUser(t.Context(), user)

	byID, err := cache.GetJSON[User](t.Context(), store, "identity:user:v1:id:user-1")
	if err != nil || byID.Username != "alice" {
		t.Fatalf("cached by ID=%+v err=%v", byID, err)
	}
	byUsername, err := cache.GetJSON[User](t.Context(), store, "identity:user:v1:username:alice")
	if err != nil || byUsername.ID != "user-1" {
		t.Fatalf("cached by username=%+v err=%v", byUsername, err)
	}
	service.invalidate(t.Context(), user)
	if _, err := store.Get(t.Context(), "identity:user:v1:id:user-1"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("ID cache after invalidation err=%v", err)
	}
	if _, err := store.Get(t.Context(), "identity:user:v1:username:alice"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("username cache after invalidation err=%v", err)
	}
}

func TestPageRejectsReversedTimeRangeBeforeDatabase(t *testing.T) {
	t.Parallel()
	from, to := time.Now(), time.Now().Add(-time.Hour)
	service := &Service{}
	ctx := platformprincipal.SystemContext(t.Context(), "tester")
	_, err := service.Page(ctx, PageInput{CreatedAtFrom: &from, CreatedAtTo: &to})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Page() error=%v", err)
	}
}

func TestDeleteRejectsTenantOwner(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	service := &Service{repository: NewRepository(sqlxDB), transactor: database.NewTransactor(sqlxDB), operations: operationStub{}, security: securityStub{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	now := time.Now()
	row := []driver.Value{"user-1", "alice", "Alice", "alice@example.com", "13800000000", StatusActive, now, "admin", now, "admin", int64(3)}
	mock.ExpectQuery(`SELECT id,username,display_name,email,phone,status,created_at,created_by,updated_at,updated_by,version FROM identity_users`).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "email", "phone", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).AddRow(row...))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenants WHERE owner_user_id=`).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	err = service.Delete(ctx, "user-1", 3)
	if !errors.Is(err, ErrInUse) {
		t.Fatalf("Delete() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
}

type operationStub struct{}

func (operationStub) Enabled() bool                                    { return true }
func (operationStub) Record(context.Context, operationlog.Entry) error { return nil }

type securityStub struct{}

func (securityStub) Enabled() bool                                   { return true }
func (securityStub) FailClosed() bool                                { return true }
func (securityStub) Record(context.Context, securitylog.Entry) error { return nil }

type memoryStore struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (s *memoryStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	if !ok {
		return nil, cache.ErrMiss
	}
	return append([]byte(nil), value...), nil
}
func (s *memoryStore) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = append([]byte(nil), value...)
	return nil
}
func (s *memoryStore) SetIfAbsent(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if _, err := s.Get(ctx, key); err == nil {
		return false, nil
	}
	return true, s.Set(ctx, key, value, ttl)
}
func (s *memoryStore) Delete(_ context.Context, keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		delete(s.values, key)
	}
	return nil
}
func (s *memoryStore) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.Get(ctx, key)
	return err == nil, nil
}
func TestUsernameValidation(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"ab", "Alice", "用户", "a space"} {
		if usernamePattern.MatchString(v) {
			t.Fatalf("username %q accepted", v)
		}
	}
}
func TestUniqueViolation(t *testing.T) {
	t.Parallel()
	if !uniqueViolation(&pgconn.PgError{Code: "23505"}) || uniqueViolation(errors.New("offline")) {
		t.Fatal("unique violation classification failed")
	}
}
