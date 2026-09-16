package datalifecycle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type lifecycleLockerStub struct {
	try func(context.Context, string, time.Duration) (cache.Lock, bool, error)
}

func (l lifecycleLockerStub) TryLock(ctx context.Context, key string, ttl time.Duration) (cache.Lock, bool, error) {
	return l.try(ctx, key, ttl)
}
func (l lifecycleLockerStub) Lock(context.Context, string, time.Duration, time.Duration) (cache.Lock, error) {
	return nil, errors.New("unexpected blocking lock")
}

func TestPartitionNameRoundTrip(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, time.September, 1, 0, 0, 0, 0, platformLocation)
	for _, parent := range []string{"operation_logs", "security_logs"} {
		name := partitionName(parent, start)
		actual, ok := parsePartitionName(parent, name)
		if !ok || !actual.Equal(start) {
			t.Fatalf("parsePartitionName(%q)=(%v,%v)", name, actual, ok)
		}
	}
}

func TestMaintainMySQLPurgesBoundedBatches(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "mysql")
	now := time.Date(2026, time.September, 16, 12, 0, 0, 0, platformLocation)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `operation_logs` WHERE occurred_at < ? ORDER BY occurred_at,id LIMIT ?")).
		WithArgs(time.Date(2025, time.September, 1, 0, 0, 0, 0, platformLocation), 250).
		WillReturnResult(sqlmock.NewResult(0, 250))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `operation_logs` WHERE occurred_at < ? ORDER BY occurred_at,id LIMIT ?")).
		WithArgs(time.Date(2025, time.September, 1, 0, 0, 0, 0, platformLocation), 250).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `security_logs` WHERE occurred_at < ? ORDER BY occurred_at,id LIMIT ?")).
		WithArgs(time.Date(2024, time.September, 1, 0, 0, 0, 0, platformLocation), 250).
		WillReturnResult(sqlmock.NewResult(0, 10))
	manager := &Manager{db: db, dialect: "mysql", cfg: config.DataLifecycle{
		PurgeBatchSize: 250, PurgeMaxBatches: 2, OperationLogRetentionMonths: 12, SecurityLogRetentionMonths: 24,
	}}
	if err := manager.maintainMySQL(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMaintainUsesBoundedSystemContextAndSkipsContention(t *testing.T) {
	t.Parallel()
	raw, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	lockCalled := false
	manager := &Manager{
		db:      sqlx.NewDb(raw, "mysql"),
		dialect: "mysql",
		cfg: config.DataLifecycle{
			Enabled: true, Timeout: time.Second, PurgeMaxBatches: 1,
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		locker: lifecycleLockerStub{try: func(ctx context.Context, key string, ttl time.Duration) (cache.Lock, bool, error) {
			lockCalled = true
			principal, ok := platformprincipal.FromContext(ctx)
			if !ok || principal.ID != "orders:data-lifecycle" || principal.Type != platformprincipal.TypeSystem {
				t.Fatalf("principal = %+v, %t", principal, ok)
			}
			if id, ok := requestid.FromContext(ctx); !ok || id == "" {
				t.Fatalf("request ID = %q, %t", id, ok)
			}
			if _, ok := ctx.Deadline(); !ok || key != "data-lifecycle:maintenance" || ttl != time.Second {
				t.Fatalf("lock context/key/ttl = %t/%q/%s", ok, key, ttl)
			}
			return nil, false, nil
		}},
		appName: "orders",
		lockTTL: time.Second,
	}
	if err := manager.Maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !lockCalled {
		t.Fatal("distributed lock was not attempted")
	}
}

func TestEnsurePartitionRefusesOccupiedDefault(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	mock.ExpectBegin()
	tx, err := db.BeginTxx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`)).
		WithArgs("operation_logs_y2026m09").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM "operation_logs_default"`).
		WithArgs(time.Date(2026, time.September, 1, 0, 0, 0, 0, platformLocation), time.Date(2026, time.October, 1, 0, 0, 0, 0, platformLocation)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	err = ensurePartition(t.Context(), tx, "operation_logs", time.Date(2026, time.September, 1, 0, 0, 0, 0, platformLocation))
	if !errors.Is(err, ErrDefaultPartitionOccupied) {
		t.Fatalf("ensurePartition() error=%v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRetainPartitionsOnlyDropsOwnedExpiredChildren(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	mock.ExpectBegin()
	tx, err := db.BeginTxx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT child.relname`).WithArgs("operation_logs").WillReturnRows(
		sqlmock.NewRows([]string{"relname"}).
			AddRow("operation_logs_y2025m01").
			AddRow("operation_logs_y2026m09").
			AddRow("operation_logs_default").
			AddRow("unowned_y2025m01"),
	)
	mock.ExpectExec(regexp.QuoteMeta(`ALTER TABLE "operation_logs" DETACH PARTITION "operation_logs_y2025m01"`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`DROP TABLE "operation_logs_y2025m01"`)).WillReturnResult(sqlmock.NewResult(0, 0))
	if err := retainPartitions(t.Context(), tx, "operation_logs", time.Date(2026, time.January, 1, 0, 0, 0, 0, platformLocation), ""); err != nil {
		t.Fatal(err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestParsePartitionNameRejectsUnownedTables(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"operation_logs_default",
		"operation_logs_y2026m13",
		"other_logs_y2026m09",
		"security_logs_y2026m09;drop_table",
	} {
		if _, ok := parsePartitionName("operation_logs", name); ok {
			t.Fatalf("parsePartitionName accepted %q", name)
		}
	}
}

func TestMonthStartUsesPlatformTimezoneBoundary(t *testing.T) {
	t.Parallel()
	value := time.Date(2026, time.October, 1, 0, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	want := time.Date(2026, time.October, 1, 0, 0, 0, 0, platformLocation)
	if actual := monthStart(value); !actual.Equal(want) {
		t.Fatalf("monthStart()=%v want=%v", actual, want)
	}
}
