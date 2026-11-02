package outbound

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
)

type registryTestLifecycle struct{}

func (registryTestLifecycle) Append(fx.Hook) {}

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
