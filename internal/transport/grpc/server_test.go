package grpctransport

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/jmoiron/sqlx"
	hellov1 "github.com/lihongjie0209/go-api-template/gen/hello/v1"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/environment"
	apphealth "github.com/lihongjie0209/go-api-template/internal/health"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	identityv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/identity/v1"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpchealth "google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestGRPCEndpointDescriptorsExactlyCoverRegisteredBusinessMethods(t *testing.T) {
	t.Parallel()
	server := grpc.NewServer()
	hellov1.RegisterHelloServiceServer(server, &helloServer{})
	identityv1.RegisterIdentityServiceServer(server, &identityServer{})
	grpc_health_v1.RegisterHealthServer(server, grpchealth.NewServer())
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := accesscontrol.NewEndpointRegistry(resources, grpcEndpointDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateCoverage(grpcBusinessOperations(server)); err != nil {
		t.Fatalf("gRPC descriptor coverage: %v", err)
	}
}

func TestGRPCHealthPublisherImplementsStandardProtocol(t *testing.T) {
	t.Parallel()
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service := apphealth.New(nil, client, config.Config{Health: config.Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second}})
	publisher := newGRPCHealthPublisher(service, "test-service")
	publisher.Start(t.Context())
	t.Cleanup(func() {
		if err := publisher.Stop(context.Background()); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	})

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	grpc_health_v1.RegisterHealthServer(server, publisher.server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	clientAPI := grpc_health_v1.NewHealthClient(connection)

	for _, name := range []string{"", "test-service", hellov1.HelloService_ServiceDesc.ServiceName} {
		response, checkErr := clientAPI.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{Service: name})
		if checkErr != nil || response.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
			t.Fatalf("Check(%q) = %v, %v", name, response, checkErr)
		}
	}
	if _, checkErr := clientAPI.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{Service: "unknown.Service"}); status.Code(checkErr) != codes.NotFound {
		t.Fatalf("unknown Check() error = %v", checkErr)
	}
	list, err := clientAPI.List(t.Context(), &grpc_health_v1.HealthListRequest{})
	if err != nil || list.GetStatuses()["test-service"].GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("List() = %v, %v", list, err)
	}
	watch, err := clientAPI.Watch(t.Context(), &grpc_health_v1.HealthCheckRequest{Service: "test-service"})
	if err != nil {
		t.Fatal(err)
	}
	update, err := watch.Recv()
	if err != nil || update.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("Watch().Recv() = %v, %v", update, err)
	}
}

func TestGRPCMetricMethodBoundsUnknownLabels(t *testing.T) {
	t.Parallel()
	if got := grpcMetricMethod(hellov1.HelloService_Ping_FullMethodName); got != hellov1.HelloService_Ping_FullMethodName {
		t.Fatalf("grpcMetricMethod(known) = %q", got)
	}
	for _, method := range []string{"", "/attacker.Service/" + strings.Repeat("x", 1024), "/grpc.health.v1.Health/attacker-controlled"} {
		if got := grpcMetricMethod(method); got != "unmatched" {
			t.Fatalf("grpcMetricMethod(%q) = %q", method, got)
		}
	}
}

func TestHelloServer_PingThroughGRPC(t *testing.T) {
	t.Parallel()
	jwtConfig, keyErr := testutil.JWTConfig()
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	rawDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })
	authService := auth.NewWithDatabase(config.Config{JWT: jwtConfig}, sqlx.NewDb(rawDB, "sqlmock"))
	token, err := authService.Issue("client")
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT count\(\*\) FROM identity_service_accounts`).WithArgs("client", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.ChainUnaryInterceptor(requestIDInterceptor, authInterceptor(authService, config.Auth{})))
	hellov1.RegisterHelloServiceServer(server, &helloServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	ctx := metadata.AppendToOutgoingContext(requestid.WithContext(t.Context(), "grpc-test-1"), "authorization", "Bearer "+token, "x-request-id", "grpc-test-1")
	var header metadata.MD
	response, err := hellov1.NewHelloServiceClient(connection).Ping(ctx, &hellov1.PingRequest{Message: "hello"}, grpc.Header(&header))
	if err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if response.GetMessage() != "hello" {
		t.Fatalf("message = %q", response.GetMessage())
	}
	if got := header.Get("x-request-id"); len(got) != 1 || got[0] != "grpc-test-1" {
		t.Fatalf("x-request-id = %v", got)
	}
}

func TestEnvironmentInterceptorInjectsActiveProfile(t *testing.T) {
	t.Parallel()
	interceptor := environmentInterceptor("test")
	_, err := interceptor(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: hellov1.HelloService_Ping_FullMethodName}, func(ctx context.Context, _ any) (any, error) {
		profile, ok := environment.FromContext(ctx)
		if !ok || profile != "test" {
			t.Fatalf("environment = %q, %v", profile, ok)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type environmentTestStream struct{ grpc.ServerStream }

func (s environmentTestStream) Context() context.Context { return context.Background() }

func TestEnvironmentStreamInterceptorInjectsActiveProfile(t *testing.T) {
	t.Parallel()
	interceptor := environmentStreamInterceptor("test")
	err := interceptor(nil, environmentTestStream{}, &grpc.StreamServerInfo{FullMethod: "/test.Service/Watch"}, func(_ any, stream grpc.ServerStream) error {
		profile, ok := environment.FromContext(stream.Context())
		if !ok || profile != "test" {
			t.Fatalf("environment = %q, %v", profile, ok)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticateGRPC_PSKWildcard(t *testing.T) {
	t.Parallel()
	const key = "01234567890123456789012345678901"
	authService := auth.New(config.Config{})
	cfg := config.Auth{PSK: config.PSK{Enabled: true, Key: key}}
	for _, test := range []struct {
		name   string
		header string
		code   codes.Code
	}{
		{name: "valid", header: "PSK " + key, code: codes.OK},
		{name: "missing credential remains anonymous", code: codes.OK},
		{name: "bearer rejected", header: "Bearer " + key, code: codes.Unauthenticated},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", test.header))
			authenticated, err := authenticateGRPC(ctx, "/hello.v1.UserService/GetUser", authService, cfg)
			if got := status.Code(err); got != test.code {
				t.Fatalf("status code = %s, want %s", got, test.code)
			}
			if strings.HasPrefix(test.header, "PSK ") {
				value, ok := platformprincipal.FromContext(authenticated)
				if !ok || value.ID != "go-api-template:psk" || value.Type != platformprincipal.TypeServiceAccount {
					t.Fatalf("principal = %#v, %v", value, ok)
				}
				scheme, schemeOK := accesscontrol.CredentialSchemeFromContext(authenticated)
				if !schemeOK || scheme != accesscontrol.CredentialSchemePSK {
					t.Fatalf("credential scheme = %q, %v", scheme, schemeOK)
				}
			}
		})
	}
}

func TestAuthenticateGRPC_JWTInjectsPrincipal(t *testing.T) {
	t.Parallel()
	jwtConfig, keyErr := testutil.JWTConfig()
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	rawDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })
	service := auth.NewWithDatabase(config.Config{JWT: jwtConfig}, sqlx.NewDb(rawDB, "sqlmock"))
	token, err := service.Issue("user-1")
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT count\(\*\) FROM identity_service_accounts`).WithArgs("user-1", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+token))
	ctx, err = authenticateGRPC(ctx, "/hello.v1.UserService/GetUser", service, config.Auth{})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := platformprincipal.FromContext(ctx)
	if !ok || value.ID != "user-1" || value.Type != platformprincipal.TypeServiceAccount {
		t.Fatalf("principal = %#v, %v", value, ok)
	}
	if scheme, schemeOK := accesscontrol.CredentialSchemeFromContext(ctx); !schemeOK || scheme != accesscontrol.CredentialSchemeBearer {
		t.Fatalf("credential scheme = %q, %v", scheme, schemeOK)
	}
}

func TestGRPCPasswordChangeRequirementBlocksBusinessCalls(t *testing.T) {
	for _, test := range []struct {
		name     string
		method   string
		claim    bool
		forced   bool
		stream   bool
		wantCode codes.Code
	}{
		{name: "unary business requires password change even with stale claim", method: hellov1.HelloService_Ping_FullMethodName, forced: true, wantCode: codes.PermissionDenied},
		{name: "stream business requires password change", method: "/example.v1.ExampleService/Watch", forced: true, stream: true, wantCode: codes.PermissionDenied},
		{name: "health remains available", method: "/grpc.health.v1.Health/Check", forced: true, wantCode: codes.OK},
		{name: "normal user can call business method despite stale claim", method: hellov1.HelloService_Ping_FullMethodName, claim: true, wantCode: codes.OK},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			jwtConfig, err := testutil.JWTConfig()
			if err != nil {
				t.Fatal(err)
			}
			rawDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rawDB.Close() })
			service := auth.NewWithDatabase(config.Config{JWT: jwtConfig}, sqlx.NewDb(rawDB, "sqlmock"))
			token, err := service.IssuePrincipalWithState(
				platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"},
				auth.TokenState{MustChangePassword: test.claim},
			)
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery(`SELECT c.must_change_password FROM identity_sessions s JOIN identity_users u`).
				WithArgs("session-1", "user-1", sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"must_change_password"}).AddRow(test.forced))
			ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+token))
			called := false
			if test.stream {
				interceptor := optionalAuthStreamInterceptor(service, config.Auth{})
				err = interceptor(nil, &contextServerStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: test.method}, func(_ any, _ grpc.ServerStream) error {
					called = true
					return nil
				})
			} else {
				interceptor := optionalAuthInterceptor(service, config.Auth{})
				_, err = interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: test.method}, func(_ context.Context, _ any) (any, error) {
					called = true
					return nil, nil
				})
			}
			if got := status.Code(err); got != test.wantCode {
				t.Fatalf("status code = %s, want %s", got, test.wantCode)
			}
			if called != (test.wantCode == codes.OK) {
				t.Fatalf("handler called = %v, want %v", called, test.wantCode == codes.OK)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGRPCEndpointDefinitionsIncludeHello(t *testing.T) {
	t.Parallel()
	endpoints := grpcEndpointDefinitions()
	if len(endpoints) != 8 || endpoints[0].Operation != hellov1.HelloService_Ping_FullMethodName {
		t.Fatalf("endpoints = %+v", endpoints)
	}
}
