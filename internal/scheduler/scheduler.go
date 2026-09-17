package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/robfig/cron/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
)

var ErrSkipped = errors.New("scheduled job skipped because its distributed lock is held")

type Manager struct {
	cfg            config.Config
	db             *sqlx.DB
	tx             *database.Transactor
	locker         cache.Locker
	metrics        *observability.Metrics
	logger         *slog.Logger
	handlers       *HandlerRegistry
	runner         *cron.Cron
	mu             sync.Mutex
	dynamicEntries map[string]cron.EntryID
	refresh        chan struct{}
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
}

func NewManager(lc fx.Lifecycle, cfg config.Config, db *sqlx.DB, tx *database.Transactor, locker cache.Locker, metrics *observability.Metrics, logger *slog.Logger, handlers *HandlerRegistry) (*Manager, error) {
	location, err := time.LoadLocation(cfg.Cron.Timezone)
	if err != nil {
		return nil, err
	}
	runner := cron.New(cron.WithLocation(location), cron.WithSeconds(), cron.WithChain(cron.Recover(cron.PrintfLogger(slogWriter{logger})), cron.SkipIfStillRunning(cron.PrintfLogger(slogWriter{logger}))))
	schedulerCtx, cancelScheduler := context.WithCancel(context.Background())
	manager := &Manager{cfg: cfg, db: db, tx: tx, locker: locker, metrics: metrics, logger: logger, handlers: handlers, runner: runner, dynamicEntries: map[string]cron.EntryID{}, refresh: make(chan struct{}, 1), ctx: schedulerCtx, cancel: cancelScheduler, done: make(chan struct{})}
	if cfg.Cron.SampleSpec != "" {
		if _, err := runner.AddFunc(cfg.Cron.SampleSpec, func() {
			started := time.Now()
			status := "success"
			if err := runSample(schedulerCtx, cfg.App.Name, cfg.Cron.JobTimeout, cfg.DistributedLock.TTL, locker, logger); err != nil {
				status = "error"
				if errors.Is(err, ErrSkipped) {
					status = "skipped"
				}
			}
			metrics.ObserveCron("sample", status, started)
		}); err != nil {
			cancelScheduler()
			return nil, err
		}
	}
	lc.Append(fx.Hook{OnStart: func(context.Context) error {
		if cfg.Cron.Enabled {
			if cfg.Database.Enabled {
				if err := manager.Refresh(schedulerCtx); err != nil {
					return err
				}
			}
			runner.Start()
			go manager.refreshLoop()
			logger.Info("scheduler started")
		} else {
			close(manager.done)
		}
		return nil
	}, OnStop: func(ctx context.Context) error {
		cancelScheduler()
		if cfg.Cron.Enabled {
			<-manager.done
		}
		stopCtx := runner.Stop()
		select {
		case <-stopCtx.Done():
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	return manager, nil
}

func (m *Manager) Notify() {
	select {
	case m.refresh <- struct{}{}:
	default:
	}
}

func (m *Manager) refreshLoop() {
	defer close(m.done)
	ticker := time.NewTicker(m.cfg.Cron.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
		case <-m.refresh:
		}
		ctx, cancel := context.WithTimeout(m.ctx, min(5*time.Second, m.cfg.Cron.RefreshInterval))
		err := m.Refresh(ctx)
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			m.logger.ErrorContext(m.ctx, "refresh scheduled jobs failed", "error", err)
		}
	}
}

func (m *Manager) Refresh(ctx context.Context) error {
	if !m.cfg.Database.Enabled {
		return nil
	}
	definitions := []Definition{}
	if err := m.db.SelectContext(ctx, &definitions, m.db.Rebind(`SELECT `+definitionColumns+` FROM scheduled_jobs WHERE status='active' AND deleted_at IS NULL ORDER BY code ASC,id ASC`)); err != nil {
		return err
	}
	for _, definition := range definitions {
		if err := validateDefinition(DefinitionInput{Code: definition.Code, Name: definition.Name, Description: definition.Description, CronSpec: definition.CronSpec, Timezone: definition.Timezone, Handler: definition.Handler, TimeoutSeconds: definition.TimeoutSeconds, LockTTLSeconds: definition.LockTTLSeconds, Status: definition.Status, Payload: definition.Payload}); err != nil {
			return err
		}
		if _, err := m.handlers.Resolve(definition.Handler); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entryID := range m.dynamicEntries {
		m.runner.Remove(entryID)
	}
	m.dynamicEntries = make(map[string]cron.EntryID, len(definitions))
	for _, item := range definitions {
		definition := item
		entryID, err := m.runner.AddFunc("CRON_TZ="+definition.Timezone+" "+definition.CronSpec, func() { _ = m.execute(m.ctx, definition, "cron") })
		if err != nil {
			return err
		}
		m.dynamicEntries[definition.ID] = entryID
	}
	m.logger.InfoContext(ctx, "scheduled jobs refreshed", "count", len(definitions))
	return nil
}

func (m *Manager) Trigger(ctx context.Context, definition Definition) error {
	return m.execute(ctx, definition, "manual")
}

func (m *Manager) execute(parent context.Context, definition Definition, source string) error {
	started, status := time.Now(), "success"
	handler, err := m.handlers.Resolve(definition.Handler)
	if err == nil {
		ctx, cancel, span := startJobContext(parent, m.cfg.App.Name, definition.Code, time.Duration(definition.TimeoutSeconds)*time.Second)
		err = runJobContext(ctx, definition.Code, time.Duration(definition.LockTTLSeconds)*time.Second, m.locker, func(ctx context.Context) error { return handler(ctx, definition.Payload) }, m.logger)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "job failed")
		}
		finished := time.Now()
		m.recordRun(ctx, definition, source, statusForRun(err), started, finished)
		span.End()
		cancel()
	}
	if err != nil {
		status = "error"
		if errors.Is(err, ErrSkipped) {
			status = "skipped"
		}
	}
	// Handler keys come from the bounded code registry; job codes are
	// administrator-defined and must never become metric label values.
	m.metrics.ObserveCron(definition.Handler, status, started)
	return err
}

func statusForRun(err error) string {
	if err == nil {
		return "success"
	}
	if errors.Is(err, ErrSkipped) {
		return "skipped"
	}
	return "error"
}

func (m *Manager) recordRun(executionCtx context.Context, definition Definition, source, status string, started, finished time.Time) {
	if m.tx == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(executionCtx), 5*time.Second)
	defer cancel()
	requestID, _ := requestid.FromContext(executionCtx)
	traceID := trace.SpanContextFromContext(executionCtx).TraceID().String()
	errorMessage := ""
	if status == "error" {
		errorMessage = "scheduled job execution failed"
	}
	duration := finished.Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	err := m.tx.Within(ctx, &sql.TxOptions{}, func(tx *sqlx.Tx) error {
		now := time.Now()
		_, execErr := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO scheduled_job_runs(id,scheduled_job_id,job_code,job_name,handler,trigger_source,status,request_id,trace_id,started_at,finished_at,duration_ms,error_message,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`), uuid.NewString(), definition.ID, definition.Code, definition.Name, definition.Handler, source, status, requestID, traceID, started, finished, duration, errorMessage, now, m.cfg.App.Name+":scheduler:"+definition.Code, now, m.cfg.App.Name+":scheduler:"+definition.Code)
		return execErr
	})
	if err != nil {
		m.logger.ErrorContext(executionCtx, "record scheduled job run failed", "handler", definition.Handler, "error", err)
	}
}

func runSample(parent context.Context, serviceName string, timeout, lockTTL time.Duration, locker cache.Locker, logger *slog.Logger) error {
	return runJob(parent, serviceName, "sample", timeout, lockTTL, locker, func(ctx context.Context) error {
		logger.InfoContext(ctx, "sample scheduled job executed", "job", "sample")
		return nil
	}, logger)
}

func runJob(parent context.Context, serviceName, job string, timeout, lockTTL time.Duration, locker cache.Locker, execute func(context.Context) error, logger *slog.Logger) error {
	ctx, cancel, span := startJobContext(parent, serviceName, job, timeout)
	defer cancel()
	defer span.End()
	err := runJobContext(ctx, job, lockTTL, locker, execute, logger)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "job failed")
	}
	return err
}

func startJobContext(parent context.Context, serviceName, job string, timeout time.Duration) (context.Context, context.CancelFunc, trace.Span) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	ctx = requestid.WithContext(platformprincipal.SystemContext(ctx, serviceName+":scheduler:"+job), requestid.Generate())
	ctx, span := otel.Tracer("go-api-template/scheduler").Start(ctx, "cron."+job)
	return ctx, cancel, span
}

func runJobContext(ctx context.Context, job string, lockTTL time.Duration, locker cache.Locker, execute func(context.Context) error, logger *slog.Logger) error {
	acquired, err := cache.TryWithLock(ctx, locker, "cron:"+job, lockTTL, execute)
	if err != nil {
		logger.ErrorContext(ctx, "scheduled job failed", "job", job, "error", err)
		return err
	}
	if !acquired {
		logger.InfoContext(ctx, "scheduled job skipped", "job", job, "reason", "lock held")
		return ErrSkipped
	}
	return nil
}

type slogWriter struct{ logger *slog.Logger }

func (w slogWriter) Printf(format string, args ...any) {
	w.logger.Error("scheduler event", "detail", format, "args", args)
}

var Module = fx.Module("scheduler", fx.Provide(NewManager), fx.Invoke(func(*Manager) {}))
