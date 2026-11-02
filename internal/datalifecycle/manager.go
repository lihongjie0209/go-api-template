package datalifecycle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/background"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"go.uber.org/fx"
)

var partitionNamePattern = regexp.MustCompile(`^(operation_logs|security_logs)_y([0-9]{4})m(0[1-9]|1[0-2])$`)
var platformLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

var ErrDefaultPartitionOccupied = errors.New("default partition contains rows for a partition that must be created")
var ErrPartitionNameOccupied = errors.New("partition name is occupied by an unattached table")
var ErrLifecycleDatabaseUnavailable = errors.New("data lifecycle database is unavailable")
var errDataLifecycleMaintenanceFailed = errors.New("data_lifecycle_maintenance_failed")

type Manager struct {
	db              *sqlx.DB
	dialect         string
	cfg             config.DataLifecycle
	logger          *slog.Logger
	metrics         *observability.Metrics
	locker          cache.Locker
	appName         string
	lockTTL         time.Duration
	lockKey         string
	advisoryLockKey string
}

func New(db *sqlx.DB, cfg config.Config, logger *slog.Logger, metrics *observability.Metrics, locker cache.Locker) *Manager {
	return &Manager{
		db:              db,
		dialect:         cfg.Database.Type,
		cfg:             cfg.DataLifecycle,
		logger:          logger,
		metrics:         metrics,
		locker:          locker,
		appName:         cfg.App.Name,
		lockTTL:         cfg.DistributedLock.TTL,
		lockKey:         "data-lifecycle:maintenance:" + cfg.Database.Type + ":" + cfg.App.Name + ":" + cfg.Database.Name + ":" + cfg.Database.Schema,
		advisoryLockKey: cfg.App.Name + ":" + cfg.Database.Name + ":" + cfg.Database.Schema + ":data-lifecycle",
	}
}

func (m *Manager) Maintain(ctx context.Context) error {
	if !m.cfg.Enabled {
		return nil
	}
	if m.db == nil {
		return ErrLifecycleDatabaseUnavailable
	}
	started := time.Now()
	actorID := m.appName + ":data-lifecycle"
	ctx = requestid.WithContext(platformprincipal.SystemContext(ctx, actorID), requestid.Generate())
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()
	acquired, err := cache.TryWithLock(ctx, m.locker, m.lockKey, m.lockTTL, func(leaseCtx context.Context) error {
		if m.dialect == "mysql" {
			return m.maintainMySQL(leaseCtx, time.Now())
		}
		return m.maintain(leaseCtx, time.Now())
	})
	status := "success"
	if !acquired && err == nil {
		status = "skipped"
		m.logger.InfoContext(ctx, "data lifecycle maintenance skipped", "reason", "lock held")
	} else if err != nil {
		status = "error"
	} else {
		m.logger.InfoContext(ctx, "data lifecycle maintenance completed", "duration", time.Since(started))
	}
	if m.metrics != nil {
		m.metrics.ObserveInfrastructure("data_lifecycle", "database", "maintain", status, started)
	}
	return err
}

func (m *Manager) maintainMySQL(ctx context.Context, now time.Time) error {
	month := monthStart(now)
	for _, policy := range []struct {
		table           string
		retentionMonths int
	}{
		{table: "operation_logs", retentionMonths: m.cfg.OperationLogRetentionMonths},
		{table: "security_logs", retentionMonths: m.cfg.SecurityLogRetentionMonths},
	} {
		cutoff := month.AddDate(0, -policy.retentionMonths, 0)
		query := `DELETE FROM ` + quoteMySQLIdentifier(policy.table) + ` WHERE occurred_at < ? ORDER BY occurred_at,id LIMIT ?`
		for range m.cfg.PurgeMaxBatches {
			result, err := m.db.ExecContext(ctx, query, cutoff, m.cfg.PurgeBatchSize)
			if err != nil {
				return fmt.Errorf("purge expired rows from %s: %w", policy.table, err)
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("read purged row count from %s: %w", policy.table, err)
			}
			if rows < int64(m.cfg.PurgeBatchSize) {
				break
			}
		}
	}
	if err := m.cleanupOutboxMySQL(ctx, now); err != nil {
		return err
	}
	return m.cleanupRefreshHistoryMySQL(ctx, now)
}

func (m *Manager) maintain(ctx context.Context, now time.Time) error {
	return database.NewTransactor(m.db).Within(ctx, nil, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, m.advisoryLockKey); err != nil {
			return fmt.Errorf("lock partition maintenance: %w", err)
		}
		if m.cfg.ArchiveSchema != "" {
			if _, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+quoteIdentifier(m.cfg.ArchiveSchema)); err != nil {
				return fmt.Errorf("create archive schema: %w", err)
			}
		}
		month := monthStart(now)
		partitionLimit := m.cfg.PartitionMaxPerRun
		if partitionLimit <= 0 {
			partitionLimit = 12
		} else if partitionLimit > 120 {
			partitionLimit = 120
		}
		for _, policy := range []struct {
			table           string
			retentionMonths int
		}{
			{table: "operation_logs", retentionMonths: m.cfg.OperationLogRetentionMonths},
			{table: "security_logs", retentionMonths: m.cfg.SecurityLogRetentionMonths},
		} {
			for offset := 0; offset <= m.cfg.PremakeMonths; offset++ {
				if err := ensurePartition(ctx, tx, policy.table, month.AddDate(0, offset, 0)); err != nil {
					return err
				}
			}
			if err := retainPartitions(ctx, tx, policy.table, month.AddDate(0, -policy.retentionMonths, 0), m.cfg.ArchiveSchema, partitionLimit); err != nil {
				return err
			}
		}
		if err := m.cleanupOutboxPostgres(ctx, tx, now); err != nil {
			return err
		}
		return m.cleanupRefreshHistoryPostgres(ctx, tx, now)
	})
}

func (m *Manager) cleanupRefreshHistoryMySQL(ctx context.Context, now time.Time) error {
	if m.cfg.PurgeBatchSize <= 0 || m.cfg.PurgeMaxBatches <= 0 {
		return nil
	}
	actor := m.appName + ":data-lifecycle"
	query := `UPDATE identity_refresh_token_history SET deleted_at=?,deleted_by=? WHERE deleted_at IS NULL AND expires_at<? ORDER BY expires_at,token_hash LIMIT ?`
	for range m.cfg.PurgeMaxBatches {
		var rows int64
		err := database.NewTransactor(m.db).Within(ctx, nil, func(tx *sqlx.Tx) error {
			result, err := tx.ExecContext(ctx, query, now, actor, now, m.cfg.PurgeBatchSize)
			if err != nil {
				return err
			}
			rows, err = result.RowsAffected()
			return err
		})
		if err != nil {
			return fmt.Errorf("expire rotated refresh token history: %w", err)
		}
		if rows < int64(m.cfg.PurgeBatchSize) {
			break
		}
	}
	return nil
}

func (m *Manager) cleanupRefreshHistoryPostgres(ctx context.Context, tx *sqlx.Tx, now time.Time) error {
	if m.cfg.PurgeBatchSize <= 0 || m.cfg.PurgeMaxBatches <= 0 {
		return nil
	}
	query := `WITH expired AS (
		SELECT token_hash FROM identity_refresh_token_history
		WHERE deleted_at IS NULL AND expires_at<$1
		ORDER BY expires_at,token_hash LIMIT $2 FOR UPDATE SKIP LOCKED
	)
	UPDATE identity_refresh_token_history target SET deleted_at=$3,deleted_by=$4
	FROM expired WHERE target.token_hash=expired.token_hash`
	for range m.cfg.PurgeMaxBatches {
		result, err := tx.ExecContext(ctx, query, now, m.cfg.PurgeBatchSize, now, m.appName+":data-lifecycle")
		if err != nil {
			return fmt.Errorf("expire rotated refresh token history: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read expired refresh token history count: %w", err)
		}
		if rows < int64(m.cfg.PurgeBatchSize) {
			break
		}
	}
	return nil
}

func (m *Manager) cleanupOutboxMySQL(ctx context.Context, now time.Time) error {
	if m.cfg.OutboxPublishedRetention <= 0 || m.cfg.OutboxDeadRetention <= 0 {
		return nil
	}
	for _, policy := range []struct {
		query  string
		cutoff time.Time
	}{
		{query: `UPDATE event_outbox SET envelope=?,trace_parent='',trace_state='',last_error='',deleted_at=?,deleted_by=? WHERE deleted_at IS NULL AND published_at IS NOT NULL AND published_at<? ORDER BY published_at,id LIMIT ?`, cutoff: now.Add(-m.cfg.OutboxPublishedRetention)},
		{query: `UPDATE event_outbox SET envelope=?,trace_parent='',trace_state='',last_error='',deleted_at=?,deleted_by=? WHERE deleted_at IS NULL AND dead_at IS NOT NULL AND dead_at<? ORDER BY dead_at,id LIMIT ?`, cutoff: now.Add(-m.cfg.OutboxDeadRetention)},
	} {
		for range m.cfg.PurgeMaxBatches {
			var rows int64
			err := database.NewTransactor(m.db).Within(ctx, nil, func(tx *sqlx.Tx) error {
				result, err := tx.ExecContext(ctx, policy.query, []byte{}, now, m.appName+":data-lifecycle", policy.cutoff, m.cfg.PurgeBatchSize)
				if err != nil {
					return err
				}
				rows, err = result.RowsAffected()
				return err
			})
			if err != nil {
				return fmt.Errorf("clean retained event outbox rows: %w", err)
			}
			if rows < int64(m.cfg.PurgeBatchSize) {
				break
			}
		}
	}
	return nil
}

func (m *Manager) cleanupOutboxPostgres(ctx context.Context, tx *sqlx.Tx, now time.Time) error {
	if m.cfg.OutboxPublishedRetention <= 0 || m.cfg.OutboxDeadRetention <= 0 {
		return nil
	}
	for _, policy := range []struct {
		column string
		cutoff time.Time
	}{
		{column: "published_at", cutoff: now.Add(-m.cfg.OutboxPublishedRetention)},
		{column: "dead_at", cutoff: now.Add(-m.cfg.OutboxDeadRetention)},
	} {
		query := `WITH expired AS (
			SELECT id FROM event_outbox
			WHERE deleted_at IS NULL AND ` + policy.column + ` IS NOT NULL AND ` + policy.column + `<$1
			ORDER BY ` + policy.column + `,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		UPDATE event_outbox target SET envelope=''::bytea,trace_parent='',trace_state='',last_error='',deleted_at=$3,deleted_by=$4
		FROM expired WHERE target.id=expired.id`
		for range m.cfg.PurgeMaxBatches {
			result, err := tx.ExecContext(ctx, query, policy.cutoff, m.cfg.PurgeBatchSize, now, m.appName+":data-lifecycle")
			if err != nil {
				return fmt.Errorf("clean retained event outbox rows: %w", err)
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("read cleaned event outbox row count: %w", err)
			}
			if rows < int64(m.cfg.PurgeBatchSize) {
				break
			}
		}
	}
	return nil
}

func ensurePartition(ctx context.Context, tx *sqlx.Tx, parent string, start time.Time) error {
	name := partitionName(parent, start)
	var state struct {
		Attached bool `db:"attached"`
		Occupied bool `db:"occupied"`
	}
	query := `SELECT EXISTS (
		SELECT 1 FROM pg_inherits inheritance
		JOIN pg_class parent ON parent.oid=inheritance.inhparent
		JOIN pg_namespace parent_namespace ON parent_namespace.oid=parent.relnamespace
		JOIN pg_class child ON child.oid=inheritance.inhrelid
		WHERE parent_namespace.nspname=current_schema() AND parent.relname=$1 AND child.relname=$2
	) AS attached,to_regclass(current_schema() || '.' || $2) IS NOT NULL AS occupied`
	if err := tx.GetContext(ctx, &state, query, parent, name); err != nil {
		return fmt.Errorf("inspect partition %s: %w", name, err)
	}
	if state.Attached {
		return nil
	}
	if state.Occupied {
		return fmt.Errorf("%w: %s", ErrPartitionNameOccupied, name)
	}
	end := start.AddDate(0, 1, 0)
	defaultTable := parent + "_default"
	var occupied bool
	check := `SELECT EXISTS (SELECT 1 FROM ` + quoteIdentifier(defaultTable) + ` WHERE occurred_at >= $1 AND occurred_at < $2)`
	if err := tx.GetContext(ctx, &occupied, check, start, end); err != nil {
		return fmt.Errorf("inspect default partition %s: %w", defaultTable, err)
	}
	if occupied {
		return fmt.Errorf("%w: %s [%s,%s)", ErrDefaultPartitionOccupied, defaultTable, start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	statement := fmt.Sprintf(
		`CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
		quoteIdentifier(name), quoteIdentifier(parent), start.Format(time.RFC3339), end.Format(time.RFC3339),
	)
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("create partition %s: %w", name, err)
	}
	return nil
}

func retainPartitions(ctx context.Context, tx *sqlx.Tx, parent string, cutoff time.Time, archiveSchema string, limit int) error {
	children := []struct {
		Schema string `db:"schema_name"`
		Name   string `db:"name"`
	}{}
	query := `SELECT child_namespace.nspname AS schema_name,child.relname AS name
		FROM pg_inherits inheritance
		JOIN pg_class parent ON parent.oid=inheritance.inhparent
		JOIN pg_namespace parent_namespace ON parent_namespace.oid=parent.relnamespace
		JOIN pg_class child ON child.oid=inheritance.inhrelid
		JOIN pg_namespace child_namespace ON child_namespace.oid=child.relnamespace
		WHERE parent_namespace.nspname=current_schema() AND parent.relname=$1
		ORDER BY child.relname,child_namespace.nspname`
	if err := tx.SelectContext(ctx, &children, query, parent); err != nil {
		return fmt.Errorf("list partitions for %s: %w", parent, err)
	}
	retained := 0
	for _, child := range children {
		start, ok := parsePartitionName(parent, child.Name)
		if !ok || start.AddDate(0, 1, 0).After(cutoff) {
			continue
		}
		if retained >= limit {
			break
		}
		qualifiedChild := quoteIdentifier(child.Schema) + `.` + quoteIdentifier(child.Name)
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+quoteIdentifier(parent)+` DETACH PARTITION `+qualifiedChild); err != nil {
			return fmt.Errorf("detach expired partition %s: %w", child.Name, err)
		}
		if archiveSchema == "" {
			if _, err := tx.ExecContext(ctx, `DROP TABLE `+qualifiedChild); err != nil {
				return fmt.Errorf("drop expired partition %s: %w", child.Name, err)
			}
			retained++
			continue
		}
		archivedName := archivePartitionName(child.Schema, child.Name)
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+qualifiedChild+` RENAME TO `+quoteIdentifier(archivedName)); err != nil {
			return fmt.Errorf("rename expired partition %s for archive: %w", child.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+quoteIdentifier(child.Schema)+`.`+quoteIdentifier(archivedName)+` SET SCHEMA `+quoteIdentifier(archiveSchema)); err != nil {
			return fmt.Errorf("archive expired partition %s: %w", child.Name, err)
		}
		retained++
	}
	return nil
}

func archivePartitionName(sourceSchema, partition string) string {
	digest := sha256.Sum256([]byte(sourceSchema))
	return fmt.Sprintf("%s_%x", partition, digest[:8])
}

func monthStart(value time.Time) time.Time {
	value = value.In(platformLocation)
	return time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, platformLocation)
}

func partitionName(parent string, start time.Time) string {
	return fmt.Sprintf("%s_y%04dm%02d", parent, start.Year(), int(start.Month()))
}

func parsePartitionName(parent, name string) (time.Time, bool) {
	matches := partitionNamePattern.FindStringSubmatch(name)
	if len(matches) != 4 || matches[1] != parent {
		return time.Time{}, false
	}
	year, yearErr := strconv.Atoi(matches[2])
	month, monthErr := strconv.Atoi(matches[3])
	if yearErr != nil || monthErr != nil {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), 1, 0, 0, 0, 0, platformLocation), true
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteMySQLIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func start(lifecycle fx.Lifecycle, manager *Manager) {
	if !manager.cfg.Enabled {
		return
	}
	worker := background.New(manager.run)
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := manager.Maintain(ctx); err != nil {
				return err
			}
			worker.Start()
			return nil
		},
		OnStop: worker.Stop,
	})
}

func (m *Manager) run(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.Maintain(ctx); err != nil && !errors.Is(err, context.Canceled) {
				m.logger.ErrorContext(ctx, "data lifecycle maintenance failed", "error", observability.SafeError(err, errDataLifecycleMaintenanceFailed))
			}
		}
	}
}

var Module = fx.Module("data-lifecycle", fx.Provide(New), fx.Invoke(start))
