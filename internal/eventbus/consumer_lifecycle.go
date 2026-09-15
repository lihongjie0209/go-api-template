package eventbus

import (
	"context"
	"errors"
	"log/slog"

	"github.com/lihongjie0209/go-api-template/internal/background"
	"go.uber.org/fx"
)

// RegisterConsumer owns a blocking consumer function through the Fx
// lifecycle. Start never waits for the consume loop; Stop cancels and joins it.
func RegisterConsumer(lifecycle fx.Lifecycle, name string, logger *slog.Logger, consume func(context.Context) error) {
	worker := background.New(func(ctx context.Context) {
		if err := consume(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("event consumer stopped", "consumer", name, "error", err)
		}
	})
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			worker.Start()
			return nil
		},
		OnStop: worker.Stop,
	})
}
