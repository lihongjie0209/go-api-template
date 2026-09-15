package operationlog

import (
	"context"
	"log/slog"

	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	platformeventbus "github.com/lihongjie0209/microservice-platform-go/eventbus"
	"go.uber.org/fx"
)

func start(lifecycle fx.Lifecycle, service *Service, logger *slog.Logger) {
	if !service.enabled {
		return
	}
	eventbus.RegisterConsumer(lifecycle, "operation-log", logger, func(ctx context.Context) error {
		return service.bus.ConsumeWithOptions(ctx, consumerOptions(service, logger))
	})
}

func consumerOptions(service *Service, logger *slog.Logger) eventbusConsumerOptions {
	return eventbusConsumerOptions{Durable: service.cfg.Durable, FilterSubject: service.cfg.Subject, Handler: service.consume, OnError: func(err error) { logger.Error("operation log consumer failed", "error", err) }}
}

type eventbusConsumerOptions = platformeventbus.ConsumerOptions

func asRecorder(service *Service) Recorder { return service }

var Module = fx.Module("operation-log", fx.Provide(New, asRecorder), fx.Invoke(start))
