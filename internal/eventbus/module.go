package eventbus

import (
	"context"
	"log/slog"

	"github.com/lihongjie0209/go-api-template/internal/background"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"go.uber.org/fx"
)

func newBus(lifecycle fx.Lifecycle, cfg config.Config) (*Bus, error) {
	bus, err := New(context.Background(), cfg)
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
