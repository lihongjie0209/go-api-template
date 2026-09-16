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
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/background"
	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/environment"
	apphealth "github.com/lihongjie0209/go-api-template/internal/health"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
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
	grpchealth "google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type Server struct {
	server          *grpc.Server
	healthPublisher *grpcHealthPublisher
	address         string
	logger          *slog.Logger
}

func NewServer(lc fx.Lifecycle, cfg config.Config, authService *auth.Service, resources *pbac.Registry, schemas *datapermission.SchemaRegistry, authorizer platformauthz.Authorizer, healthService *apphealth.Service, identityService *identity.Service, idempotencyManager *idempotency.Manager, metrics *observability.Metrics, logger *slog.Logger) (*Server, error) {
	definitions := grpcEndpointDefinitions()
	for _, endpoint := range definitions {
		if err := schemas.ValidateEndpoint(endpoint); err != nil {
			return nil, fmt.Errorf("validate grpc data permission: %w", err)
		}
	}
	endpointRegistry, err := accesscontrol.NewEndpointRegistry(resources, definitions)
	if err != nil {
		return nil, fmt.Errorf("build grpc authorization registry: %w", err)
	}
	enforcer := accesscontrol.NewEnforcer(endpointRegistry, authorizer)
	options := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(cfg.GRPC.MaxReceiveBytes),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(environmentInterceptor(cfg.Runtime.ActiveProfile), requestIDInterceptor, idempotencyInterceptor, recoveryInterceptor(logger), optionalAuthInterceptor(authService, cfg.Auth), operationAuthorizationInterceptor(enforcer), platformidempotency.UnaryServerInterceptor(idempotencyManager, cfg.Idempotency.GRPCMethods, logger), metricsInterceptor(metrics, logger)),
		grpc.ChainStreamInterceptor(environmentStreamInterceptor(cfg.Runtime.ActiveProfile), requestIDStreamInterceptor, idempotencyStreamInterceptor, recoveryStreamInterceptor(logger), optionalAuthStreamInterceptor(authService, cfg.Auth), operationAuthorizationStreamInterceptor(enforcer), metricsStreamInterceptor(metrics, logger)),
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
	healthPublisher := newGRPCHealthPublisher(healthService, cfg.App.Name)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthPublisher.server)
	if cfg.GRPC.ReflectionEnabled {
		reflection.Register(grpcServer)
	}
	if err := endpointRegistry.ValidateCoverage(grpcBusinessOperations(grpcServer)); err != nil {
		return nil, fmt.Errorf("validate grpc authorization descriptor coverage: %w", err)
	}
	server := &Server{server: grpcServer, healthPublisher: healthPublisher, address: cfg.GRPC.Address, logger: logger}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if err := server.start(cfg.GRPC.Enabled)(ctx); err != nil {
			return err
		}
		if cfg.GRPC.Enabled {
			healthPublisher.Start(ctx)
		}
		return nil
	}, OnStop: server.stop})
	return server, nil
}

func grpcBusinessOperations(server *grpc.Server) []accesscontrol.Operation {
	if server == nil {
		return nil
	}
	operations := []accesscontrol.Operation{}
	for service, info := range server.GetServiceInfo() {
		for _, method := range info.Methods {
			name := "/" + service + "/" + method.Name
			if isInfrastructureMethod(name) {
				continue
			}
			operations = append(operations, accesscontrol.Operation{Transport: accesscontrol.TransportGRPC, Name: name})
		}
	}
	return operations
}

func grpcEndpointDefinitions() []accesscontrol.Endpoint {
	return []accesscontrol.Endpoint{
		{Transport: accesscontrol.TransportGRPC, Operation: hellov1.HelloService_Ping_FullMethodName, Authentication: accesscontrol.AuthenticationPublic, DataPermission: accesscontrol.DataPermissionNone},
		{Transport: accesscontrol.TransportGRPC, Operation: identityv1.IdentityService_GetUser_FullMethodName, Authentication: accesscontrol.AuthenticationService, Resource: "identity.user", Action: "read", DataPermission: accesscontrol.DataPermissionNone, DataPermissionReason: "platform-scoped resource; operation PBAC is the complete authorization boundary"},
		{Transport: accesscontrol.TransportGRPC, Operation: identityv1.IdentityService_BatchGetUsers_FullMethodName, Authentication: accesscontrol.AuthenticationService, Resource: "identity.user", Action: "list", DataPermission: accesscontrol.DataPermissionNone, DataPermissionReason: "platform-scoped resource; operation PBAC is the complete authorization boundary"},
		{Transport: accesscontrol.TransportGRPC, Operation: identityv1.IdentityService_ListUsers_FullMethodName, Authentication: accesscontrol.AuthenticationService, Resource: "identity.user", Action: "list", DataPermission: accesscontrol.DataPermissionNone, DataPermissionReason: "platform-scoped resource; operation PBAC is the complete authorization boundary"},
		{Transport: accesscontrol.TransportGRPC, Operation: identityv1.IdentityService_ValidateSession_FullMethodName, Authentication: accesscontrol.AuthenticationService, Resource: "identity.internal-authentication", Action: "validate-session", DataPermission: accesscontrol.DataPermissionNone, DataPermissionReason: "platform-scoped internal resource; operation PBAC is the complete authorization boundary"},
		{Transport: accesscontrol.TransportGRPC, Operation: identityv1.IdentityService_RevokeTenantSessions_FullMethodName, Authentication: accesscontrol.AuthenticationService, Resource: "identity.internal-authentication", Action: "revoke-tenant-sessions", DataPermission: accesscontrol.DataPermissionNone, DataPermissionReason: "platform-scoped internal resource; operation PBAC is the complete authorization boundary"},
		{Transport: accesscontrol.TransportGRPC, Operation: identityv1.IdentityService_IssueTenantToken_FullMethodName, Authentication: accesscontrol.AuthenticationService, Resource: "identity.internal-authentication", Action: "issue-tenant-token", DataPermission: accesscontrol.DataPermissionNone, DataPermissionReason: "platform-scoped internal resource; operation PBAC is the complete authorization boundary"},
		{Transport: accesscontrol.TransportGRPC, Operation: identityv1.IdentityService_GetServiceAccount_FullMethodName, Authentication: accesscontrol.AuthenticationService, Resource: "identity.service-account", Action: "read", DataPermission: accesscontrol.DataPermissionNone, DataPermissionReason: "platform-scoped resource; operation PBAC is the complete authorization boundary"},
	}
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
	var healthErr error
	if s.healthPublisher != nil {
		healthErr = s.healthPublisher.Stop(ctx)
	}
	stopped := make(chan struct{})
	go func() { s.server.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
		return healthErr
	case <-ctx.Done():
		s.server.Stop()
		return errors.Join(healthErr, ctx.Err())
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

const grpcHealthRefreshInterval = time.Second

// grpcHealthPublisher delegates protocol semantics (unknown services, List and
// streaming Watch) to gRPC's maintained health implementation. One shared
// worker probes dependencies, avoiding a database/Redis polling loop per Watch
// client.
type grpcHealthPublisher struct {
	server *grpchealth.Server
	health *apphealth.Service
	names  []string
	worker *background.Worker
}

func newGRPCHealthPublisher(service *apphealth.Service, appName string) *grpcHealthPublisher {
	publisher := &grpcHealthPublisher{
		server: grpchealth.NewServer(),
		health: service,
		names: []string{
			"",
			appName,
			hellov1.HelloService_ServiceDesc.ServiceName,
			identityv1.IdentityService_ServiceDesc.ServiceName,
		},
	}
	for _, name := range publisher.names {
		publisher.server.SetServingStatus(name, grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	}
	publisher.worker = background.New(publisher.run)
	return publisher
}

func (p *grpcHealthPublisher) Start(ctx context.Context) {
	p.refresh(ctx)
	p.worker.Start()
}

func (p *grpcHealthPublisher) Stop(ctx context.Context) error {
	p.server.Shutdown()
	return p.worker.Stop(ctx)
}

func (p *grpcHealthPublisher) run(ctx context.Context) {
	ticker := time.NewTicker(grpcHealthRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.refresh(ctx)
		}
	}
}

func (p *grpcHealthPublisher) refresh(ctx context.Context) {
	_, ready := p.health.Ready(ctx)
	serving := grpc_health_v1.HealthCheckResponse_NOT_SERVING
	if ready {
		serving = grpc_health_v1.HealthCheckResponse_SERVING
	}
	for _, name := range p.names {
		p.server.SetServingStatus(name, serving)
	}
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

func operationAuthorizationInterceptor(enforcer *accesscontrol.Enforcer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isInfrastructureMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		endpoint, err := enforceGRPCOperation(ctx, info.FullMethod, enforcer)
		if err != nil {
			return nil, err
		}
		return handler(accesscontrol.WithEndpoint(ctx, endpoint), req)
	}
}

func enforceGRPCOperation(ctx context.Context, method string, enforcer *accesscontrol.Enforcer) (accesscontrol.Endpoint, error) {
	endpoint, err := enforcer.Authorize(ctx, accesscontrol.TransportGRPC, method)
	if err != nil {
		switch {
		case errors.Is(err, accesscontrol.ErrEndpointMissing):
			return accesscontrol.Endpoint{}, status.Error(codes.Internal, "authorization policy is not configured")
		case errors.Is(err, accesscontrol.ErrAuthentication):
			return accesscontrol.Endpoint{}, status.Error(codes.Unauthenticated, "authentication is required")
		case errors.Is(err, platformauthz.ErrDecisionUnavailable):
			return accesscontrol.Endpoint{}, status.Error(codes.Unavailable, "authorization decision is unavailable")
		default:
			return accesscontrol.Endpoint{}, status.Error(codes.PermissionDenied, "permission denied")
		}
	}
	return endpoint, nil
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

func operationAuthorizationStreamInterceptor(enforcer *accesscontrol.Enforcer) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !isInfrastructureMethod(info.FullMethod) {
			endpoint, err := enforceGRPCOperation(stream.Context(), info.FullMethod, enforcer)
			if err != nil {
				return err
			}
			stream = &contextServerStream{ServerStream: stream, ctx: accesscontrol.WithEndpoint(stream.Context(), endpoint)}
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
			method := grpcMetricMethod(info.FullMethod)
			metrics.GRPCRequests.WithLabelValues(method, code.String()).Inc()
			metrics.GRPCDuration.WithLabelValues(method).Observe(time.Since(started).Seconds())
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
			method := grpcMetricMethod(info.FullMethod)
			metrics.GRPCRequests.WithLabelValues(method, code.String()).Inc()
			metrics.GRPCDuration.WithLabelValues(method).Observe(time.Since(started).Seconds())
		}
		span := trace.SpanFromContext(ctx).SpanContext()
		requestID, _ := requestid.FromContext(ctx)
		logger.InfoContext(ctx, "grpc request", "request_id", requestID, "trace_id", span.TraceID().String(), "span_id", span.SpanID().String(), "method", info.FullMethod, "code", code.String(), "duration", time.Since(started))
		return response, err
	}
}

var knownGRPCMetricMethods = func() map[string]struct{} {
	methods := make(map[string]struct{})
	for _, descriptor := range []*grpc.ServiceDesc{
		&hellov1.HelloService_ServiceDesc,
		&identityv1.IdentityService_ServiceDesc,
		&grpc_health_v1.Health_ServiceDesc,
	} {
		for _, method := range descriptor.Methods {
			methods["/"+descriptor.ServiceName+"/"+method.MethodName] = struct{}{}
		}
		for _, stream := range descriptor.Streams {
			methods["/"+descriptor.ServiceName+"/"+stream.StreamName] = struct{}{}
		}
	}
	// Reflection is registered dynamically by gRPC and has two standardized
	// protocol versions. Keep both exact names rather than accepting an
	// attacker-controlled suffix as a Prometheus label.
	methods["/grpc.reflection.v1.ServerReflection/ServerReflectionInfo"] = struct{}{}
	methods["/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo"] = struct{}{}
	return methods
}()

func grpcMetricMethod(method string) string {
	if _, ok := knownGRPCMetricMethods[method]; ok {
		return method
	}
	return "unmatched"
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
