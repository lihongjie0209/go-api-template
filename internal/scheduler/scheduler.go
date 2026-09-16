package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/robfig/cron/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/fx"
)

var ErrSkipped = errors.New("scheduled job skipped because its distributed lock is held")

func New(lc fx.Lifecycle, cfg config.Config, locker cache.Locker, metrics *observability.Metrics, logger *slog.Logger) (*cron.Cron, error) {
	location, err := time.LoadLocation(cfg.Cron.Timezone)
	if err != nil {
		return nil, err
	}
	runner := cron.New(cron.WithLocation(location), cron.WithSeconds(), cron.WithChain(cron.Recover(cron.PrintfLogger(slogWriter{logger})), cron.SkipIfStillRunning(cron.PrintfLogger(slogWriter{logger}))))
	schedulerCtx, cancelScheduler := context.WithCancel(context.Background())
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
			runner.Start()
			logger.Info("scheduler started")
		}
		return nil
	}, OnStop: func(ctx context.Context) error {
		cancelScheduler()
		stopCtx := runner.Stop()
		select {
		case <-stopCtx.Done():
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	return runner, nil
}

func runSample(parent context.Context, serviceName string, timeout, lockTTL time.Duration, locker cache.Locker, logger *slog.Logger) error {
	return runJob(parent, serviceName, "sample", timeout, lockTTL, locker, func(ctx context.Context) error {
		logger.InfoContext(ctx, "sample scheduled job executed", "job", "sample")
		return nil
	}, logger)
}

func runJob(parent context.Context, serviceName, job string, timeout, lockTTL time.Duration, locker cache.Locker, execute func(context.Context) error, logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	jobRequestID := requestid.Generate()
	ctx = requestid.WithContext(platformprincipal.SystemContext(ctx, serviceName+":scheduler:"+job), jobRequestID)
	ctx, span := otel.Tracer("go-api-template/scheduler").Start(ctx, "cron."+job)
	defer span.End()
	acquired, err := cache.TryWithLock(ctx, locker, "cron:"+job, lockTTL, execute)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "job failed")
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

var Module = fx.Module("scheduler", fx.Provide(New), fx.Invoke(func(*cron.Cron) {}))
