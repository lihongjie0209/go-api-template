package grpctransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	hellov1 "github.com/lihongjie0209/go-api-template/gen/hello/v1"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/environment"
	apphealth "github.com/lihongjie0209/go-api-template/internal/health"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	"github.com/lihongjie0209/go-api-template/internal/routepolicy"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformidempotency "github.com/lihongjie0209/microservice-platform-go/idempotency"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	identityv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/identity/v1"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type Server struct {
	server  *grpc.Server
	address string
	logger  *slog.Logger
}

func NewServer(lc fx.Lifecycle, cfg config.Config, authService *auth.Service, authorizer platformauthz.Authorizer, policies *routepolicy.Manager, routeRepository *routepolicy.Repository, healthService *apphealth.Service, identityService *identity.Service, idempotencyManager *idempotency.Manager, metrics *observability.Metrics, logger *slog.Logger) (*Server, error) {
	options := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(cfg.GRPC.MaxReceiveBytes),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(environmentInterceptor(cfg.Runtime.ActiveProfile), requestIDInterceptor, idempotencyInterceptor, recoveryInterceptor(logger), optionalAuthInterceptor(authService, cfg.Auth), routePolicyInterceptor(cfg.Authorization.Enabled, cfg.App.Name, policies, authorizer), platformidempotency.UnaryServerInterceptor(idempotencyManager, cfg.Idempotency.GRPCMethods, logger), metricsInterceptor(metrics, logger)),
		grpc.ChainStreamInterceptor(environmentStreamInterceptor(cfg.Runtime.ActiveProfile), requestIDStreamInterceptor, idempotencyStreamInterceptor, recoveryStreamInterceptor(logger), optionalAuthStreamInterceptor(authService, cfg.Auth), routePolicyStreamInterceptor(cfg.Authorization.Enabled, cfg.App.Name, policies, authorizer), metricsStreamInterceptor(metrics, logger)),
	}
	if cfg.GRPC.TLS.Enabled {
		creds, err := serverCredentials(cfg.GRPC.TLS)
		if err != nil {
			return nil, err
		}
		options = append(options, grpc.Creds(creds))
	}
	grpcServer := grpc.NewServer(options...)
	hellov1.RegisterHelloServiceServer(grpcServer, &helloServer{})
	identityv1.RegisterIdentityServiceServer(grpcServer, &identityServer{service: identityService})
	grpc_health_v1.RegisterHealthServer(grpcServer, &healthServer{health: healthService})
	if cfg.GRPC.ReflectionEnabled {
		reflection.Register(grpcServer)
	}
	server := &Server{server: grpcServer, address: cfg.GRPC.Address, logger: logger}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if cfg.GRPC.Enabled && cfg.Authorization.Enabled {
			routes, err := grpcBusinessRoutes(cfg.App.Name)
			if err != nil {
				return err
			}
			if err := routeRepository.SyncRoutes(ctx, routes, cfg.App.Name+":route-discovery"); err != nil {
				return fmt.Errorf("sync grpc route definitions: %w", err)
			}
			if err := policies.Refresh(ctx); err != nil {
				return fmt.Errorf("load grpc route policies: %w", err)
			}
			if err := policies.ValidateRoutes(ctx, cfg.App.Name); err != nil {
				logger.Warn("one or more gRPC methods have no active database policy; affected calls will be denied", "error", err)
			}
		}
		return server.start(cfg.GRPC.Enabled)(ctx)
	}, OnStop: server.stop})
	return server, nil
}

func grpcBusinessRoutes(serviceName string) ([]routepolicy.Route, error) {
	methods := []string{
		hellov1.HelloService_Ping_FullMethodName,
		identityv1.IdentityService_GetUser_FullMethodName,
		identityv1.IdentityService_BatchGetUsers_FullMethodName,
		identityv1.IdentityService_ListUsers_FullMethodName,
	}
	routes := make([]routepolicy.Route, 0, len(methods))
	for _, method := range methods {
		route, err := routepolicy.NewRoute("grpc", "call", method, serviceName, buildinfo.Version)
		if err != nil {
			return nil, fmt.Errorf("describe grpc route %s: %w", method, err)
		}
		route.Operation = method
		routes = append(routes, route)
	}
	return routes, nil
}

func (s *Server) start(enabled bool) func(context.Context) error {
	return func(context.Context) error {
		if !enabled {
			s.logger.Warn("grpc server is disabled")
			return nil
		}
		listener, err := net.Listen("tcp", s.address)
		if err != nil {
			return fmt.Errorf("listen grpc: %w", err)
		}
		go func() {
			if err := s.server.Serve(listener); err != nil {
				s.logger.Error("grpc server stopped unexpectedly", "error", err)
			}
		}()
		s.logger.Info("grpc server started", "address", s.address)
		return nil
	}
}
func (s *Server) stop(ctx context.Context) error {
	stopped := make(chan struct{})
	go func() { s.server.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		s.server.Stop()
		return ctx.Err()
	}
}

type helloServer struct {
	hellov1.UnimplementedHelloServiceServer
}

func (*helloServer) Ping(ctx context.Context, request *hellov1.PingRequest) (*hellov1.PingResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if strings.TrimSpace(request.GetMessage()) == "" {
		return nil, status.Error(codes.InvalidArgument, "message is required")
	}
	return &hellov1.PingResponse{Message: request.GetMessage(), Version: buildinfo.Version}, nil
}

type healthServer struct {
	grpc_health_v1.UnimplementedHealthServer
	health *apphealth.Service
}

func (s *healthServer) Check(ctx context.Context, _ *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	_, ready := s.health.Ready(ctx)
	serving := grpc_health_v1.HealthCheckResponse_NOT_SERVING
	if ready {
		serving = grpc_health_v1.HealthCheckResponse_SERVING
	}
	return &grpc_health_v1.HealthCheckResponse{Status: serving}, nil
}
func (s *healthServer) List(context.Context, *grpc_health_v1.HealthListRequest) (*grpc_health_v1.HealthListResponse, error) {
	return &grpc_health_v1.HealthListResponse{Statuses: map[string]*grpc_health_v1.HealthCheckResponse{"": {Status: grpc_health_v1.HealthCheckResponse_SERVING}}}, nil
}

func requestIDInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	id := ""
	if values := metadata.ValueFromIncomingContext(ctx, "x-request-id"); len(values) > 0 && requestid.Valid(values[0]) {
		id = values[0]
	}
	if id == "" {
		id = requestid.Generate()
	}
	header := metadata.Pairs("x-request-id", id)
	_ = grpc.SetHeader(ctx, header)
	return handler(requestid.WithContext(ctx, id), req)
}
func idempotencyInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	values := metadata.ValueFromIncomingContext(ctx, "idempotency-key")
	if len(values) == 0 {
		return handler(ctx, req)
	}
	if !idempotency.Valid(values[0]) {
		return nil, status.Error(codes.InvalidArgument, "invalid idempotency-key")
	}
	return handler(idempotency.WithContext(ctx, values[0]), req)
}

func environmentInterceptor(profile string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(environment.WithContext(ctx, profile), req)
	}
}
func authInterceptor(service *auth.Service, cfg config.Auth) grpc.UnaryServerInterceptor {
	return optionalAuthInterceptor(service, cfg)
}

func optionalAuthInterceptor(service *auth.Service, cfg config.Auth) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		authenticated, err := authenticateGRPCOptional(ctx, service, cfg)
		if err != nil {
			return nil, err
		}
		return handler(authenticated, req)
	}
}

func authenticateGRPCOptional(ctx context.Context, service *auth.Service, cfg config.Auth) (context.Context, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return ctx, nil
	}
	header := strings.TrimSpace(values[0])
	scheme, raw, ok := strings.Cut(header, " ")
	if !ok || raw == "" {
		return nil, status.Error(codes.Unauthenticated, "invalid authorization credential")
	}
	var identity platformprincipal.Principal
	switch {
	case strings.EqualFold(scheme, "Bearer"):
		verified, err := service.Verify(ctx, raw)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "invalid or expired token")
		}
		identity = verified
	case strings.EqualFold(scheme, "PSK"):
		if !cfg.PSK.Enabled || !auth.VerifyPSK(header, cfg.PSK.Key) {
			return nil, status.Error(codes.Unauthenticated, "invalid PSK")
		}
		identity = platformprincipal.Principal{ID: "go-api-template:psk", Type: platformprincipal.TypeServiceAccount}
	default:
		return nil, status.Error(codes.Unauthenticated, "unsupported authorization scheme")
	}
	authenticated := platformprincipal.WithContext(ctx, identity)
	return platformauthz.WithCallerCredential(authenticated, header), nil
}

func routePolicyInterceptor(enabled bool, serviceName string, policies *routepolicy.Manager, authorizer platformauthz.Authorizer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !enabled || isInfrastructureMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		if err := evaluateGRPCPolicy(ctx, info.FullMethod, serviceName, policies, authorizer); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func evaluateGRPCPolicy(ctx context.Context, method, serviceName string, policies *routepolicy.Manager, authorizer platformauthz.Authorizer) error {
	if err := policies.EvaluateRoute(ctx, "grpc", "call", method, serviceName, authorizer); err != nil {
		switch {
		case errors.Is(err, routepolicy.ErrMissing):
			return status.Error(codes.Internal, "authorization policy is not configured")
		case errors.Is(err, platformauthz.ErrDecisionUnavailable):
			return status.Error(codes.Unavailable, "authorization decision is unavailable")
		default:
			return status.Error(codes.PermissionDenied, "permission denied")
		}
	}
	return nil
}

func isInfrastructureMethod(method string) bool {
	return strings.HasPrefix(method, "/grpc.health.v1.Health/") || strings.HasPrefix(method, "/grpc.reflection.")
}

func authenticateGRPC(ctx context.Context, method string, service *auth.Service, cfg config.Auth) (context.Context, error) {
	_ = method
	return authenticateGRPCOptional(ctx, service, cfg)
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextServerStream) Context() context.Context { return s.ctx }

func environmentStreamInterceptor(profile string) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &contextServerStream{ServerStream: stream, ctx: environment.WithContext(stream.Context(), profile)})
	}
}

func requestIDStreamInterceptor(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx := stream.Context()
	id := ""
	if values := metadata.ValueFromIncomingContext(ctx, "x-request-id"); len(values) > 0 && requestid.Valid(values[0]) {
		id = values[0]
	}
	if id == "" {
		id = requestid.Generate()
	}
	if err := stream.SetHeader(metadata.Pairs("x-request-id", id)); err != nil {
		return status.Error(codes.Internal, "set request metadata")
	}
	return handler(srv, &contextServerStream{ServerStream: stream, ctx: requestid.WithContext(ctx, id)})
}

func idempotencyStreamInterceptor(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	values := metadata.ValueFromIncomingContext(stream.Context(), "idempotency-key")
	if len(values) == 0 {
		return handler(srv, stream)
	}
	if !idempotency.Valid(values[0]) {
		return status.Error(codes.InvalidArgument, "invalid idempotency-key")
	}
	return handler(srv, &contextServerStream{ServerStream: stream, ctx: idempotency.WithContext(stream.Context(), values[0])})
}

func optionalAuthStreamInterceptor(service *auth.Service, cfg config.Auth) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := authenticateGRPCOptional(stream.Context(), service, cfg)
		if err != nil {
			return err
		}
		return handler(srv, &contextServerStream{ServerStream: stream, ctx: ctx})
	}
}

func routePolicyStreamInterceptor(enabled bool, serviceName string, policies *routepolicy.Manager, authorizer platformauthz.Authorizer) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !enabled && isInfrastructureMethod(info.FullMethod) {
			return handler(srv, stream)
		}
		if enabled && !isInfrastructureMethod(info.FullMethod) {
			if err := evaluateGRPCPolicy(stream.Context(), info.FullMethod, serviceName, policies, authorizer); err != nil {
				return err
			}
		}
		return handler(srv, stream)
	}
}

func recoveryStreamInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(stream.Context(), "grpc stream panic recovered", "method", info.FullMethod, "panic", recovered)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(srv, stream)
	}
}

func metricsStreamInterceptor(metrics *observability.Metrics, logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		started := time.Now()
		err := handler(srv, stream)
		code := status.Code(err)
		if metrics.Enabled() {
			metrics.GRPCRequests.WithLabelValues(info.FullMethod, code.String()).Inc()
			metrics.GRPCDuration.WithLabelValues(info.FullMethod).Observe(time.Since(started).Seconds())
		}
		requestID, _ := requestid.FromContext(stream.Context())
		logger.InfoContext(stream.Context(), "grpc stream", "request_id", requestID, "method", info.FullMethod, "code", code.String(), "duration", time.Since(started))
		return err
	}
}

func recoveryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (response any, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(ctx, "grpc panic recovered", "method", info.FullMethod, "panic", recovered)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}
func metricsInterceptor(metrics *observability.Metrics, logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		started := time.Now()
		response, err := handler(ctx, req)
		code := status.Code(err)
		if metrics.Enabled() {
			metrics.GRPCRequests.WithLabelValues(info.FullMethod, code.String()).Inc()
			metrics.GRPCDuration.WithLabelValues(info.FullMethod).Observe(time.Since(started).Seconds())
		}
		span := trace.SpanFromContext(ctx).SpanContext()
		requestID, _ := requestid.FromContext(ctx)
		logger.InfoContext(ctx, "grpc request", "request_id", requestID, "trace_id", span.TraceID().String(), "span_id", span.SpanID().String(), "method", info.FullMethod, "code", code.String(), "duration", time.Since(started))
		return response, err
	}
}

func serverCredentials(cfg config.GRPCTLS) (credentials.TransportCredentials, error) {
	certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load grpc certificate: %w", err)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	if cfg.ClientCAFile != "" {
		pem, err := os.ReadFile(cfg.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read grpc client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("parse grpc client CA")
		}
		tlsConfig.ClientCAs = pool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return credentials.NewTLS(tlsConfig), nil
}

var Module = fx.Module("grpc", fx.Provide(NewServer), fx.Invoke(func(*Server) {}))
