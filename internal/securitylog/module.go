package securitylog

import (
	"context"
	"log/slog"

	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	platformeventbus "github.com/lihongjie0209/microservice-platform-go/eventbus"
	"go.uber.org/fx"
)

func start(lifecycle fx.Lifecycle, service *Service, logger *slog.Logger) {
	if !service.Enabled() {
		return
	}
	eventbus.RegisterConsumer(lifecycle, "security-log", logger, func(ctx context.Context) error {
		return service.bus.ConsumeWithOptions(ctx, platformeventbus.ConsumerOptions{Durable: service.cfg.Durable, FilterSubject: service.cfg.Subject, Handler: service.consume, OnError: func(err error) { logger.Error("security log consumer failed", "error", err) }})
	})
}
func asRecorder(service *Service) Recorder { return service }

var Module = fx.Module("security-log", fx.Provide(New, asRecorder), fx.Invoke(start))
