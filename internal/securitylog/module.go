package securitylog

import (
	"context"
	"log/slog"

	platformeventbus "github.com/lihongjie0209/microservice-platform-go/eventbus"
	"go.uber.org/fx"
)

func start(lifecycle fx.Lifecycle, service *Service, logger *slog.Logger) {
	if !service.Enabled() {
		return
	}
	var cancel context.CancelFunc
	lifecycle.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		consumerCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
		cancel = stop
		return service.bus.ConsumeWithOptions(consumerCtx, platformeventbus.ConsumerOptions{Durable: service.cfg.Durable, FilterSubject: service.cfg.Subject, Handler: service.consume, OnError: func(err error) { logger.Error("security log consumer failed", "error", err) }})
	}, OnStop: func(context.Context) error {
		if cancel != nil {
			cancel()
		}
		return nil
	}})
}
func asRecorder(service *Service) Recorder { return service }

var Module = fx.Module("security-log", fx.Provide(New, asRecorder), fx.Invoke(start))
