package eventbus

import (
	"context"
	"log/slog"

	"github.com/lihongjie0209/go-api-template/internal/background"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"go.uber.org/fx"
)

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
		return nil, err
	}
	lifecycle.Append(fx.StopHook(func() error { return Close(bus) }))
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
