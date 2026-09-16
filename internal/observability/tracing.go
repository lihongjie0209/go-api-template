package observability

import (
	"context"
	"fmt"

	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/fx"
)

type Tracing struct {
	enabled  bool
	provider *sdktrace.TracerProvider
}

func (t *Tracing) Enabled() bool { return t.enabled }

func NewTracing(lc fx.Lifecycle, cfg config.Config) (*Tracing, error) {
	tracing := &Tracing{enabled: cfg.Observability.TracingEnabled}
	if !tracing.enabled {
		return tracing, nil
	}
	exporter, err := otlptracehttp.New(context.Background(), otlptracehttp.WithEndpointURL(cfg.Observability.TracingEndpoint))
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	res, err := traceResource(cfg)
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.Observability.TracingSampleRatio))))
	tracing.provider = provider
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(tracePropagator())
	lc.Append(fx.StopHook(func(ctx context.Context) error {
		err := provider.Shutdown(ctx)
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
		return err
	}))
	return tracing, nil
}

func tracePropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
}

func traceResource(cfg config.Config) (*resource.Resource, error) {
	customResource := resource.NewSchemaless(
		attribute.String("service.name", cfg.App.Name),
		attribute.String("service.version", buildinfo.Version),
		attribute.String("service.namespace", cfg.App.Schema),
		attribute.String("deployment.environment.name", cfg.Runtime.ActiveProfile),
		attribute.String("vcs.ref.head.revision", buildinfo.Commit),
		attribute.String("service.build.time", buildinfo.BuildTime),
	)
	res, err := resource.Merge(resource.Default(), customResource)
	if err != nil {
		return nil, fmt.Errorf("merge OpenTelemetry resource: %w", err)
	}
	return res, nil
}
