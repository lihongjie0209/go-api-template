package eventbus

import (
	"context"
	"errors"
	"log/slog"

	"github.com/lihongjie0209/go-api-template/internal/background"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"go.uber.org/fx"
)

var ErrStartupUnavailable = errors.New("event_bus_startup_unavailable")
var errShutdownUnavailable = errors.New("event_bus_shutdown_unavailable")

func newBus(lifecycle fx.Lifecycle, cfg config.Config) (*Bus, error) {
	return newBusWithFactory(lifecycle, cfg, New)
}

func newBusWithFactory(lifecycle fx.Lifecycle, cfg config.Config, factory func(context.Context, config.Config) (*Bus, error)) (*Bus, error) {
	if !cfg.EventBus.Enabled {
		return nil, nil
	}
	startupCtx, cancel := context.WithTimeout(context.Background(), cfg.EventBus.StartupTimeout)
	defer cancel()
	bus, err := factory(startupCtx, cfg)
	if err != nil {
		if ctxErr := startupCtx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, observability.SafeError(err, ErrStartupUnavailable)
	}
	if err := startupCtx.Err(); err != nil {
		if bus != nil {
			_ = Close(bus)
		}
		return nil, err
	}
	if bus == nil {
		return nil, observability.SafeError(ErrStartupUnavailable, ErrStartupUnavailable)
	}
	lifecycle.Append(fx.StopHook(func() error { return observability.SafeError(Close(bus), errShutdownUnavailable) }))
	return bus, nil
}

func startOutbox(lifecycle fx.Lifecycle, outbox *Outbox, logger *slog.Logger) {
	worker := background.New(outbox.Run)
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			worker.Start()
			return nil
		},
		OnStop: worker.Stop,
	})
}

var Module = fx.Module("event-bus", fx.Provide(newBus, NewOutbox), fx.Invoke(startOutbox))
