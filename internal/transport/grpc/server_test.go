package grpctransport

import (
	"context"
	"net"
	"strings"
	"testing"

	hellov1 "github.com/lihongjie0209/go-api-template/gen/hello/v1"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/environment"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestHelloServer_PingThroughGRPC(t *testing.T) {
	t.Parallel()
	jwtConfig, keyErr := testutil.JWTConfig()
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	authService := auth.New(config.Config{JWT: jwtConfig})
	token, err := authService.Issue("client")
	if err != nil {
		t.Fatal(err)
	}
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
	service := auth.New(config.Config{JWT: jwtConfig})
	token, err := service.Issue("user-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+token))
	ctx, err = authenticateGRPC(ctx, "/hello.v1.UserService/GetUser", service, config.Auth{})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := platformprincipal.FromContext(ctx)
	if !ok || value.ID != "user-1" || value.Type != platformprincipal.TypeServiceAccount {
		t.Fatalf("principal = %#v, %v", value, ok)
	}
}

func TestGRPCBusinessRoutesIncludeHello(t *testing.T) {
	t.Parallel()
	routes, err := grpcBusinessRoutes("test-service")
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 4 || routes[0].Path != hellov1.HelloService_Ping_FullMethodName {
		t.Fatalf("routes = %+v", routes)
	}
}
