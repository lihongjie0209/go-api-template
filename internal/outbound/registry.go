package outbound

import (
	"context"
	"errors"
	"sync"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/grpcclient"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"go.uber.org/fx"
	"google.golang.org/grpc"
)

type Registry struct {
	http map[string]*HTTPClient
	grpc map[string]*grpc.ClientConn
	mu   sync.RWMutex
}

var (
	ErrHTTPClientInit  = errors.New("outbound_http_client_init_failed")
	ErrGRPCClientInit  = errors.New("outbound_grpc_client_init_failed")
	ErrGRPCClientClose = errors.New("outbound_grpc_client_close_failed")
)

func NewRegistry(lc fx.Lifecycle, cfg config.Config, metrics *observability.Metrics) (*Registry, error) {
	registry := &Registry{http: make(map[string]*HTTPClient, len(cfg.Outbound.HTTP)), grpc: make(map[string]*grpc.ClientConn, len(cfg.Outbound.GRPC))}
	for name, upstream := range cfg.Outbound.HTTP {
		client, err := NewHTTPClient(name, upstream, metrics)
		if err != nil {
			_ = registry.close()
			return nil, observability.SafeError(err, ErrHTTPClientInit)
		}
		registry.http[name] = client
	}
	for name, upstream := range cfg.Outbound.GRPC {
		clientCfg := grpcclient.Config{Name: name, Target: upstream.Target, Timeout: upstream.Timeout, Retry: upstream.Retry, Breaker: upstream.Breaker, Metrics: metrics, TLS: grpcclient.TLSConfig{Enabled: upstream.TLS.Enabled, AllowInsecureToken: upstream.TLS.AllowInsecure, ServerName: upstream.TLS.ServerName, CAFile: upstream.TLS.CAFile, CertFile: upstream.TLS.CertFile, KeyFile: upstream.TLS.KeyFile}}
		switch upstream.Auth.Type {
		case "bearer":
			clientCfg.Token = upstream.Auth.Token
		case "psk":
			clientCfg.PSK = upstream.Auth.Token
		}
		connection, err := grpcclient.Dial(clientCfg)
		if err != nil {
			_ = registry.close()
			return nil, observability.SafeError(err, ErrGRPCClientInit)
		}
		registry.grpc[name] = connection
	}
	lc.Append(fx.StopHook(func(context.Context) error { return registry.close() }))
	return registry, nil
}

func (r *Registry) HTTP(name string) (*HTTPClient, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	client, ok := r.http[name]
	return client, ok
}
func (r *Registry) GRPC(name string) (*grpc.ClientConn, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	connection, ok := r.grpc[name]
	return connection, ok
}
func (r *Registry) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, client := range r.http {
		client.CloseIdleConnections()
	}
	var closeErr error
	for _, connection := range r.grpc {
		if err := connection.Close(); err != nil && closeErr == nil {
			closeErr = observability.SafeError(err, ErrGRPCClientClose)
		}
	}
	return closeErr
}

var Module = fx.Module("outbound", fx.Provide(NewRegistry), fx.Invoke(func(*Registry) {}))
