package operationlog

import (
	"context"
	"log/slog"

	platformeventbus "github.com/lihongjie0209/microservice-platform-go/eventbus"
	"go.uber.org/fx"
)

func start(lifecycle fx.Lifecycle, service *Service, logger *slog.Logger) {
	if !service.enabled {
		return
	}
	var cancel context.CancelFunc
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			consumerCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
			cancel = stop
			return service.bus.ConsumeWithOptions(consumerCtx, consumerOptions(service, logger))
		},
		OnStop: func(context.Context) error {
			if cancel != nil {
				cancel()
			}
			return nil
		},
	})
}

func consumerOptions(service *Service, logger *slog.Logger) eventbusConsumerOptions {
	return eventbusConsumerOptions{Durable: service.cfg.Durable, FilterSubject: service.cfg.Subject, Handler: service.consume, OnError: func(err error) { logger.Error("operation log consumer failed", "error", err) }}
}

type eventbusConsumerOptions = platformeventbus.ConsumerOptions

func asRecorder(service *Service) Recorder { return service }

var Module = fx.Module("operation-log", fx.Provide(New, asRecorder), fx.Invoke(start))
