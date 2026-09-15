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
	if cfg.Cron.SampleSpec != "" {
		if _, err := runner.AddFunc(cfg.Cron.SampleSpec, func() {
			started := time.Now()
			status := "success"
			if err := runSample(cfg.App.Name, locker, logger); err != nil {
				status = "error"
				if errors.Is(err, ErrSkipped) {
					status = "skipped"
				}
			}
			metrics.ObserveCron("sample", status, started)
		}); err != nil {
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

func runSample(serviceName string, locker cache.Locker, logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	jobRequestID := requestid.Generate()
	ctx = requestid.WithContext(platformprincipal.SystemContext(ctx, serviceName+":scheduler:sample"), jobRequestID)
	ctx, span := otel.Tracer("go-api-template/scheduler").Start(ctx, "cron.sample")
	defer span.End()
	acquired, err := cache.TryWithLock(ctx, locker, "cron:sample", time.Minute, func(leaseCtx context.Context) error {
		logger.InfoContext(leaseCtx, "sample scheduled job executed", "job", "sample", "request_id", jobRequestID)
		return nil
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "job failed")
		logger.ErrorContext(ctx, "scheduled job failed", "job", "sample", "request_id", jobRequestID, "error", err)
		return err
	}
	if !acquired {
		logger.InfoContext(ctx, "sample scheduled job skipped", "job", "sample", "request_id", jobRequestID, "reason", "lock held")
		return ErrSkipped
	}
	return nil
}

type slogWriter struct{ logger *slog.Logger }

func (w slogWriter) Printf(format string, args ...any) {
	w.logger.Error("scheduler event", "detail", format, "args", args)
}

var Module = fx.Module("scheduler", fx.Provide(New), fx.Invoke(func(*cron.Cron) {}))
