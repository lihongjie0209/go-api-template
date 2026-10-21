package eventbus

import (
	"context"
	"errors"
	"log/slog"

	"github.com/lihongjie0209/go-api-template/internal/background"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"go.uber.org/fx"
)

var errEventConsumerFailed = errors.New("event_consumer_failed")

// RegisterConsumer owns a blocking consumer function through the Fx
// lifecycle. Start never waits for the consume loop; Stop cancels and joins it.
func RegisterConsumer(lifecycle fx.Lifecycle, name string, logger *slog.Logger, consume func(context.Context) error) {
	worker := background.New(func(ctx context.Context) {
		if err := consume(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logConsumerFailure(ctx, logger, name, err)
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

func logConsumerFailure(ctx context.Context, logger *slog.Logger, name string, err error) {
	logger.ErrorContext(ctx, "event consumer stopped", "consumer", name, "error", observability.SafeError(err, errEventConsumerFailed))
}
