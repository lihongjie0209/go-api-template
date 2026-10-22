package datalifecycle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type cancelOnLogWriter struct {
	output *bytes.Buffer
	cancel context.CancelFunc
}

func (w cancelOnLogWriter) Write(data []byte) (int, error) {
	n, err := w.output.Write(data)
	w.cancel()
	return n, err
}

func TestLifecycleWorkerDoesNotLogLockErrorDetails(t *testing.T) {
	t.Parallel()
	raw, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var output bytes.Buffer
	manager := &Manager{
		db: sqlx.NewDb(raw, "mysql"), dialect: "mysql", appName: "orders", lockTTL: time.Second,
		cfg:    config.DataLifecycle{Enabled: true, Interval: time.Millisecond, Timeout: time.Second},
		logger: slog.New(slog.NewJSONHandler(cancelOnLogWriter{output: &output, cancel: cancel}, nil)),
		locker: lifecycleLockerStub{try: func(context.Context, string, time.Duration) (cache.Lock, bool, error) {
			return nil, false, errors.New("redis password=secret-value")
		}},
	}
	manager.run(ctx)
	if strings.Contains(output.String(), "secret-value") || !strings.Contains(output.String(), "data_lifecycle_maintenance_failed") {
		t.Fatalf("lifecycle worker log leaked lock details or omitted safe code: %s", output.String())
	}
}

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
	mock.ExpectBegin()
	mock.ExpectExec("SET @app_actor_id = \\?").WithArgs("orders:data-lifecycle").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE identity_refresh_token_history SET deleted_at=`).WithArgs(now, "orders:data-lifecycle", now, 250).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET @app_actor_id = NULL").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	manager := &Manager{db: db, dialect: "mysql", appName: "orders", cfg: config.DataLifecycle{
		PurgeBatchSize: 250, PurgeMaxBatches: 2, OperationLogRetentionMonths: 12, SecurityLogRetentionMonths: 24,
	}}
	if err := manager.maintainMySQL(platformprincipal.SystemContext(t.Context(), "orders:data-lifecycle"), now); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupRefreshHistoryPostgresUsesBoundedAuditedBatches(t *testing.T) {
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
	now := time.Date(2026, time.September, 16, 12, 0, 0, 0, platformLocation)
	mock.ExpectExec(`WITH expired AS`).WithArgs(now, 100, now, "orders:data-lifecycle").WillReturnResult(sqlmock.NewResult(0, 100))
	mock.ExpectExec(`WITH expired AS`).WithArgs(now, 100, now, "orders:data-lifecycle").WillReturnResult(sqlmock.NewResult(0, 0))
	manager := &Manager{appName: "orders", cfg: config.DataLifecycle{PurgeBatchSize: 100, PurgeMaxBatches: 2}}
	if err := manager.cleanupRefreshHistoryPostgres(t.Context(), tx, now); err != nil {
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

func TestCleanupRefreshHistoryMySQLUsesBoundedAuditedBatches(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "mysql")
	now := time.Date(2026, time.September, 16, 12, 0, 0, 0, platformLocation)
	actor := "orders:data-lifecycle"
	for _, rows := range []int64{100, 0} {
		mock.ExpectBegin()
		mock.ExpectExec("SET @app_actor_id = \\?").WithArgs(actor).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`UPDATE identity_refresh_token_history SET deleted_at=`).WithArgs(now, actor, now, 100).WillReturnResult(sqlmock.NewResult(0, rows))
		mock.ExpectExec("SET @app_actor_id = NULL").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
	}
	manager := &Manager{db: db, dialect: "mysql", appName: "orders", cfg: config.DataLifecycle{PurgeBatchSize: 100, PurgeMaxBatches: 2}}
	if err := manager.cleanupRefreshHistoryMySQL(platformprincipal.SystemContext(t.Context(), actor), now); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupOutboxMySQLUsesBoundedAuditedBatches(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "mysql")
	now := time.Date(2026, time.September, 16, 12, 0, 0, 0, platformLocation)
	actor := "orders:data-lifecycle"
	mock.ExpectBegin()
	mock.ExpectExec("SET @app_actor_id = \\?").WithArgs(actor).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE event_outbox SET envelope=`).
		WithArgs([]byte{}, now, actor, now.Add(-7*24*time.Hour), now.Add(-90*24*time.Hour), 250).
		WillReturnResult(sqlmock.NewResult(0, 10))
	mock.ExpectExec("SET @app_actor_id = NULL").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	manager := &Manager{db: db, dialect: "mysql", appName: "orders", cfg: config.DataLifecycle{
		PurgeBatchSize: 250, PurgeMaxBatches: 2, OutboxPublishedRetention: 7 * 24 * time.Hour, OutboxDeadRetention: 90 * 24 * time.Hour,
	}}
	if err := manager.cleanupOutboxMySQL(platformprincipal.SystemContext(t.Context(), actor), now); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupOutboxPostgresUsesBoundedBatches(t *testing.T) {
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
	now := time.Date(2026, time.September, 16, 12, 0, 0, 0, platformLocation)
	mock.ExpectExec(`WITH expired AS`).
		WithArgs(now.Add(-7*24*time.Hour), now.Add(-90*24*time.Hour), 250, now, "orders:data-lifecycle").
		WillReturnResult(sqlmock.NewResult(0, 250))
	mock.ExpectExec(`WITH expired AS`).
		WithArgs(now.Add(-7*24*time.Hour), now.Add(-90*24*time.Hour), 250, now, "orders:data-lifecycle").
		WillReturnResult(sqlmock.NewResult(0, 0))
	manager := &Manager{appName: "orders", cfg: config.DataLifecycle{
		PurgeBatchSize: 250, PurgeMaxBatches: 2, OutboxPublishedRetention: 7 * 24 * time.Hour, OutboxDeadRetention: 90 * 24 * time.Hour,
	}}
	if err := manager.cleanupOutboxPostgres(t.Context(), tx, now); err != nil {
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
			if _, ok := ctx.Deadline(); !ok || key != "data-lifecycle:maintenance:mysql:orders:platform:orders" || ttl != time.Second {
				t.Fatalf("lock context/key/ttl = %t/%q/%s", ok, key, ttl)
			}
			return nil, false, nil
		}},
		appName: "orders",
		lockTTL: time.Second,
		lockKey: "data-lifecycle:maintenance:mysql:orders:platform:orders",
	}
	if err := manager.Maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !lockCalled {
		t.Fatal("distributed lock was not attempted")
	}
}

func TestMaintenanceLockIsScopedToServiceAndDatabase(t *testing.T) {
	t.Parallel()
	raw, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "mysql")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := config.Config{
		App:             config.App{Name: "orders"},
		Database:        config.Database{Type: "mysql", Name: "platform", Schema: "orders"},
		DataLifecycle:   config.DataLifecycle{Enabled: true, Timeout: time.Second},
		DistributedLock: config.DistributedLock{TTL: time.Second},
	}
	configs := []struct {
		name   string
		change func(*config.Config)
	}{
		{name: "same target"},
		{name: "same target replica"},
		{name: "another service", change: func(cfg *config.Config) { cfg.App.Name = "billing" }},
		{name: "another database", change: func(cfg *config.Config) { cfg.Database.Name = "billing" }},
		{name: "another schema", change: func(cfg *config.Config) { cfg.Database.Schema = "billing" }},
		{name: "another dialect", change: func(cfg *config.Config) { cfg.Database.Type = "postgres" }},
	}
	keys := make(map[string]string, len(configs))
	for _, test := range configs {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			if test.change != nil {
				test.change(&cfg)
			}
			manager := New(db, cfg, logger, nil, lifecycleLockerStub{try: func(_ context.Context, key string, _ time.Duration) (cache.Lock, bool, error) {
				keys[test.name] = key
				return nil, false, nil
			}})
			if err := manager.Maintain(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
	if keys["same target"] == "" {
		t.Fatal("maintenance did not attempt the distributed lock")
	}
	if keys["same target replica"] != keys["same target"] {
		t.Errorf("replicas of the same storage target use different maintenance locks: %q and %q", keys["same target"], keys["same target replica"])
	}
	for _, test := range configs[2:] {
		if keys[test.name] == keys["same target"] {
			t.Errorf("%s shares maintenance lock key %q with another storage target", test.name, keys[test.name])
		}
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
