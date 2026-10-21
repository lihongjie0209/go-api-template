package securitylog

import (
	"context"
	"log/slog"

	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	platformeventbus "github.com/lihongjie0209/microservice-platform-go/eventbus"
	"go.uber.org/fx"
)

func start(lifecycle fx.Lifecycle, service *Service, logger *slog.Logger, metrics *observability.Metrics) {
	if !service.Enabled() {
		return
	}
	eventbus.RegisterConsumer(lifecycle, "security-log", logger, metrics, func(ctx context.Context) error {
		return service.bus.ConsumeWithOptions(ctx, consumerOptions(service, logger))
	})
}

func consumerOptions(service *Service, logger *slog.Logger) platformeventbus.ConsumerOptions {
	return platformeventbus.ConsumerOptions{Durable: service.cfg.Durable, FilterSubject: service.cfg.Subject, Handler: service.consume, OnError: eventbus.ConsumerErrorHandler(logger, "security-log")}
}
func asRecorder(service *Service) Recorder                           { return service }
func asTransactionalRecorder(service *Service) TransactionalRecorder { return service }

var Module = fx.Module("security-log", fx.Provide(New, asRecorder, asTransactionalRecorder), fx.Invoke(start))
