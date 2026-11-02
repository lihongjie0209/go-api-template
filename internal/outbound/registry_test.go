package outbound

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/fx"
)

type registryTestLifecycle struct{}

func (registryTestLifecycle) Append(fx.Hook) {}

type tracingRegistryLifecycle struct{ hooks []fx.Hook }

func (l *tracingRegistryLifecycle) Append(hook fx.Hook) { l.hooks = append(l.hooks, hook) }

func TestOutboundHTTPTracesAfterFxStart(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	collector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	lifecycle := new(tracingRegistryLifecycle)
	_, err := observability.NewTracing(lifecycle, config.Config{Observability: config.Observability{TracingEnabled: true, TracingEndpoint: collector.URL, TracingSampleRatio: 1}})
	require.NoError(t, err)
	client, err := NewHTTPClient("inventory", validHTTPUpstream(upstream.URL), nil)
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)
	require.Len(t, lifecycle.hooks, 1)
	started := false
	t.Cleanup(func() {
		if started {
			_ = lifecycle.hooks[0].OnStop(context.Background())
		}
	})
	require.NoError(t, lifecycle.hooks[0].OnStart(t.Context()))
	started = true
	provider, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	require.True(t, ok)
	recorder := tracetest.NewSpanRecorder()
	provider.RegisterSpanProcessor(recorder)
	response, err := client.Do(t.Context(), http.MethodGet, "/trace", nil, nil)
	if response != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	require.NotEmpty(t, recorder.Ended(), "outbound HTTP client captured an obsolete tracer provider")
}

func TestRegistryClassifiesClientInitializationFailures(t *testing.T) {
	t.Parallel()
	missingTLSFile := filepath.Join(t.TempDir(), "secret-value.pem")
	for _, test := range []struct {
		name string
		cfg  config.Config
		want error
	}{
		{
			name: "HTTP TLS file path",
			cfg: config.Config{Outbound: config.Outbound{HTTP: map[string]config.HTTPUpstream{
				"inventory": func() config.HTTPUpstream {
					cfg := validHTTPUpstream("https://example.com")
					cfg.TLS = config.ClientTLS{Enabled: true, CAFile: missingTLSFile}
					return cfg
				}(),
			}}},
			want: ErrHTTPClientInit,
		},
		{
			name: "invalid HTTP client name",
			cfg: config.Config{Outbound: config.Outbound{HTTP: map[string]config.HTTPUpstream{
				"token=secret-value": validHTTPUpstream("https://example.com"),
			}}},
			want: ErrHTTPClientInit,
		},
		{
			name: "gRPC TLS file path",
			cfg: config.Config{Outbound: config.Outbound{GRPC: map[string]config.GRPCUpstream{
				"inventory": {
					Target: "dns:///inventory:9090", Timeout: time.Second,
					Retry: config.Retry{MaxAttempts: 1, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond},
					TLS:   config.ClientTLS{Enabled: true, CAFile: missingTLSFile},
				},
			}}},
			want: ErrGRPCClientInit,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			registry, err := NewRegistry(registryTestLifecycle{}, test.cfg, nil)
			require.Nil(t, registry)
			require.ErrorIs(t, err, test.want)
			require.False(t, strings.Contains(err.Error(), "secret-value"), "startup error exposed configuration: %v", err)
		})
	}
}
