package migration

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lihongjie0209/go-api-template/internal/config"
)

func TestRunRejectsUnknownDirectionBeforeOpeningDatabase(t *testing.T) {
	t.Parallel()
	err := Run(config.Migration{}, "sideways", 0)
	if err == nil || !strings.Contains(err.Error(), "unsupported migration direction") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestWithMigrationTable(t *testing.T) {
	t.Parallel()
	result, err := withMigrationOptions("postgres://user:pass@db/app?sslmode=disable", "orders_db", "orders_schema_migrations", "orders")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(result)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("x-migrations-table"); got != "orders_schema_migrations" {
		t.Fatalf("x-migrations-table = %q", got)
	}
	if got := parsed.Query().Get("sslmode"); got != "disable" {
		t.Fatalf("sslmode = %q", got)
	}
	if got := parsed.Query().Get("search_path"); got != "orders" {
		t.Fatalf("search_path = %q", got)
	}
	if parsed.Path != "/orders_db" {
		t.Fatalf("database path = %q", parsed.Path)
	}
}

func TestCreateSchemaSerializesConcurrentBootstrap(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\(\$1\)\)`).WithArgs("go-api-template:schema:orders").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "orders"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := createSchema(context.Background(), (*sql.DB)(db), "orders"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateSchemaRejectsCanceledContextBeforeAcquiringLock(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := createSchema(ctx, db, "orders"); !errors.Is(err, context.Canceled) {
		t.Fatalf("createSchema() error = %v, want context cancellation", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateSchemaRollsBackWhenLockFails(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	lockErr := errors.New("lock unavailable")
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\(\$1\)\)`).WithArgs("go-api-template:schema:orders").WillReturnError(lockErr)
	mock.ExpectRollback()
	if err := createSchema(context.Background(), db, "orders"); !errors.Is(err, lockErr) {
		t.Fatalf("createSchema() error = %v, want lock error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWithMigrationTableMySQLDSN(t *testing.T) {
	t.Parallel()
	result, err := withMigrationOptions("mysql://app:app@tcp(mysql:3306)/app", "orders_db", "orders_schema_migrations", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	const expected = "mysql://app:app@tcp(mysql:3306)/orders_db?x-migrations-table=orders_schema_migrations"
	if result != expected {
		t.Fatalf("result = %q, want %q", result, expected)
	}
}

func TestWithMigrationLockTimeoutPreservesPostgresOptions(t *testing.T) {
	t.Parallel()
	for _, scheme := range []string{"postgres", "postgresql"} {
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()
			original := scheme + "://app:secret@db/orders?sslmode=verify-full&search_path=orders&x-migrations-table=orders_schema_migrations&lock_timeout=0"
			result, err := withMigrationLockTimeout(original, 45*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(result)
			if err != nil {
				t.Fatal(err)
			}
			query := parsed.Query()
			if query.Get("lock_timeout") != "45000" || query.Get("sslmode") != "verify-full" || query.Get("search_path") != "orders" || query.Get("x-migrations-table") != "orders_schema_migrations" {
				t.Fatalf("migration query options = %v", query)
			}
		})
	}
}

func TestWithMigrationLockTimeoutPreservesMySQLURL(t *testing.T) {
	t.Parallel()
	const original = "mysql://app:secret@tcp(db:3306)/orders?x-migrations-table=orders_schema_migrations"
	result, err := withMigrationLockTimeout(original, 30*time.Second)
	if err != nil || result != original {
		t.Fatalf("withMigrationLockTimeout() = %q, %v", result, err)
	}
}

func TestWithMigrationLockTimeoutRejectsConflictingConnectionOptions(t *testing.T) {
	t.Parallel()
	_, err := withMigrationLockTimeout("postgres://db/orders?options=-c+lock_timeout%3D0", 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "lock_timeout") {
		t.Fatalf("withMigrationLockTimeout() error = %v, want conflicting option", err)
	}
}

func TestRunRejectsOutOfRangeLockTimeoutBeforeOpeningDatabase(t *testing.T) {
	t.Parallel()
	err := Run(config.Migration{Path: ".", DatabaseURL: "postgres://db/orders", LockTimeout: 6 * time.Minute}, "up", 0)
	if err == nil || !strings.Contains(err.Error(), "migration.lock_timeout") {
		t.Fatalf("Run() error = %v, want lock timeout error", err)
	}
}
