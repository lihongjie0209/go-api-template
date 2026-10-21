package eventbus

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/background"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"go.uber.org/fx"
)

var errEventConsumerFailed = errors.New("event_consumer_failed")
var errEventConsumerStopped = errors.New("event_consumer_stopped")

const consumerRetryMin = time.Second
const consumerRetryMax = 30 * time.Second

// RegisterConsumer owns a blocking consumer function through the Fx
// lifecycle. Name must be a bounded code-owned constant because it labels the
// restart metric. Start never waits for the consume loop; Stop cancels and joins it.
func RegisterConsumer(lifecycle fx.Lifecycle, name string, logger *slog.Logger, metrics *observability.Metrics, consume func(context.Context) error) {
	worker := background.New(func(ctx context.Context) {
		runConsumer(ctx, name, logger, metrics, consume)
	})
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			worker.Start()
			return nil
		},
		OnStop: worker.Stop,
	})
}

func runConsumer(ctx context.Context, name string, logger *slog.Logger, metrics *observability.Metrics, consume func(context.Context) error) {
	backoff := consumerRetryMin
	for ctx.Err() == nil {
		started := time.Now()
		err := consume(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errEventConsumerStopped
		}
		logConsumerFailure(ctx, logger, name, err)
		if time.Since(started) >= time.Minute {
			backoff = consumerRetryMin
		}
		timer := time.NewTimer(jitterConsumerDelay(backoff))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		metrics.ObserveEventConsumerRestart(name)
		backoff = min(backoff*2, consumerRetryMax)
	}
}

func jitterConsumerDelay(backoff time.Duration) time.Duration {
	half := backoff / 2
	return half + time.Duration(rand.Int64N(int64(backoff-half)+1))
}

func logConsumerFailure(ctx context.Context, logger *slog.Logger, name string, err error) {
	logger.ErrorContext(ctx, "event consumer retrying", "consumer", name, "error", observability.SafeError(err, errEventConsumerFailed))
}

// ConsumerErrorHandler logs asynchronous consumer failures without exposing
// handler errors, payloads, or dependency connection details.
func ConsumerErrorHandler(logger *slog.Logger, name string) func(error) {
	return func(err error) {
		logger.Error("event consumer processing failed", "consumer", name, "error", observability.SafeError(err, errEventConsumerFailed))
	}
}
