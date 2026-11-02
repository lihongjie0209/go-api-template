package observability

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
)

type Tracing struct {
	enabled  bool
	provider *sdktrace.TracerProvider
}

var (
	errTracingStartupFailed  = errors.New("tracing_startup_failed")
	errTracingShutdownFailed = errors.New("tracing_shutdown_failed")
)

func (t *Tracing) Enabled() bool { return t.enabled }

func NewTracing(lc fx.Lifecycle, cfg config.Config) (*Tracing, error) {
	return newTracingWithExporter(lc, cfg, func(ctx context.Context, endpoint string) (sdktrace.SpanExporter, error) {
		return otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
	})
}

func newTracingWithExporter(lc fx.Lifecycle, cfg config.Config, create func(context.Context, string) (sdktrace.SpanExporter, error)) (*Tracing, error) {
	tracing := &Tracing{enabled: cfg.Observability.TracingEnabled}
	if !tracing.enabled {
		return tracing, nil
	}
	var previousProvider trace.TracerProvider
	var previousPropagator propagation.TextMapPropagator
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			exporter, err := create(ctx, cfg.Observability.TracingEndpoint)
			if err != nil {
				return SafeError(err, errTracingStartupFailed)
			}
			if missingSpanExporter(exporter) {
				return errTracingStartupFailed
			}
			if err := ctx.Err(); err != nil {
				shutdownUnusedExporter(ctx, exporter)
				return err
			}
			res, err := traceResource(cfg)
			if err != nil {
				shutdownUnusedExporter(ctx, exporter)
				return SafeError(err, errTracingStartupFailed)
			}
			provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.Observability.TracingSampleRatio))))
			tracing.provider = provider
			previousProvider = otel.GetTracerProvider()
			previousPropagator = otel.GetTextMapPropagator()
			otel.SetTracerProvider(provider)
			otel.SetTextMapPropagator(tracePropagator())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if tracing.provider == nil {
				return nil
			}
			err := tracing.provider.Shutdown(ctx)
			otel.SetTracerProvider(previousProvider)
			otel.SetTextMapPropagator(previousPropagator)
			tracing.provider = nil
			return SafeError(err, errTracingShutdownFailed)
		},
	})
	return tracing, nil
}

func shutdownUnusedExporter(ctx context.Context, exporter sdktrace.SpanExporter) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = exporter.Shutdown(cleanupCtx)
}

func missingSpanExporter(exporter sdktrace.SpanExporter) bool {
	if exporter == nil {
		return true
	}
	value := reflect.ValueOf(exporter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
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
