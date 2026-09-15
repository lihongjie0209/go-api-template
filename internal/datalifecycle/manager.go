package datalifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"go.uber.org/fx"
)

var partitionNamePattern = regexp.MustCompile(`^(operation_logs|security_logs)_y([0-9]{4})m(0[1-9]|1[0-2])$`)
var platformLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

var ErrDefaultPartitionOccupied = errors.New("default partition contains rows for a partition that must be created")

type Manager struct {
	db      *sqlx.DB
	dialect string
	cfg     config.DataLifecycle
	logger  *slog.Logger
	metrics *observability.Metrics
}

func New(db *sqlx.DB, cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) *Manager {
	return &Manager{db: db, dialect: cfg.Database.Type, cfg: cfg.DataLifecycle, logger: logger, metrics: metrics}
}

func (m *Manager) Maintain(ctx context.Context) error {
	if !m.cfg.Enabled || m.db == nil {
		return nil
	}
	started := time.Now()
	var err error
	if m.dialect == "mysql" {
		err = m.maintainMySQL(ctx, time.Now())
	} else {
		err = m.maintain(ctx, time.Now())
	}
	status := "success"
	if err != nil {
		status = "error"
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
		if _, err := m.db.ExecContext(ctx, query, cutoff, m.cfg.PurgeBatchSize); err != nil {
			return fmt.Errorf("purge expired rows from %s: %w", policy.table, err)
		}
	}
	return nil
}

func (m *Manager) maintain(ctx context.Context, now time.Time) error {
	tx, err := m.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin partition maintenance: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "go-api-template:data-lifecycle"); err != nil {
		return fmt.Errorf("lock partition maintenance: %w", err)
	}
	if m.cfg.ArchiveSchema != "" {
		if _, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+quoteIdentifier(m.cfg.ArchiveSchema)); err != nil {
			return fmt.Errorf("create archive schema: %w", err)
		}
	}
	month := monthStart(now)
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
		if err := retainPartitions(ctx, tx, policy.table, month.AddDate(0, -policy.retentionMonths, 0), m.cfg.ArchiveSchema); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit partition maintenance: %w", err)
	}
	return nil
}

func ensurePartition(ctx context.Context, tx *sqlx.Tx, parent string, start time.Time) error {
	name := partitionName(parent, start)
	var exists bool
	if err := tx.GetContext(ctx, &exists, `SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`, name); err != nil {
		return fmt.Errorf("inspect partition %s: %w", name, err)
	}
	if exists {
		return nil
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

func retainPartitions(ctx context.Context, tx *sqlx.Tx, parent string, cutoff time.Time, archiveSchema string) error {
	children := []string{}
	query := `SELECT child.relname
		FROM pg_inherits inheritance
		JOIN pg_class parent ON parent.oid=inheritance.inhparent
		JOIN pg_namespace parent_namespace ON parent_namespace.oid=parent.relnamespace
		JOIN pg_class child ON child.oid=inheritance.inhrelid
		WHERE parent_namespace.nspname=current_schema() AND parent.relname=$1`
	if err := tx.SelectContext(ctx, &children, query, parent); err != nil {
		return fmt.Errorf("list partitions for %s: %w", parent, err)
	}
	for _, child := range children {
		start, ok := parsePartitionName(parent, child)
		if !ok || start.AddDate(0, 1, 0).After(cutoff) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+quoteIdentifier(parent)+` DETACH PARTITION `+quoteIdentifier(child)); err != nil {
			return fmt.Errorf("detach expired partition %s: %w", child, err)
		}
		if archiveSchema == "" {
			if _, err := tx.ExecContext(ctx, `DROP TABLE `+quoteIdentifier(child)); err != nil {
				return fmt.Errorf("drop expired partition %s: %w", child, err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+quoteIdentifier(child)+` SET SCHEMA `+quoteIdentifier(archiveSchema)); err != nil {
			return fmt.Errorf("archive expired partition %s: %w", child, err)
		}
	}
	return nil
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
	if !manager.cfg.Enabled || manager.db == nil {
		return
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := manager.Maintain(ctx); err != nil {
				return err
			}
			go manager.run(workerCtx)
			return nil
		},
		OnStop: func(context.Context) error {
			cancel()
			return nil
		},
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
				m.logger.ErrorContext(ctx, "data lifecycle maintenance failed", "error", err)
			}
		}
	}
}

var Module = fx.Module("data-lifecycle", fx.Provide(New), fx.Invoke(start))
