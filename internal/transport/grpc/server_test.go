package grpctransport

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	hellov1 "github.com/lihongjie0209/go-api-template/gen/hello/v1"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

type fakeGRPCIdempotencyManager struct {
	decision    idempotency.Decision
	fingerprint string
	completed   *cachedGRPCResponse
	failed      *idempotency.Failure
}

func (*fakeGRPCIdempotencyManager) Enabled() bool { return true }
func (m *fakeGRPCIdempotencyManager) Begin(_ context.Context, _, fingerprint string) (idempotency.Decision, error) {
	m.fingerprint = fingerprint
	return m.decision, nil
}
func (m *fakeGRPCIdempotencyManager) Complete(_ context.Context, _, _ string, response any) error {
	value, ok := response.(cachedGRPCResponse)
	if ok {
		m.completed = &value
	}
	return nil
}
func (m *fakeGRPCIdempotencyManager) Fail(_ context.Context, _, _ string, failure idempotency.Failure) error {
	m.failed = &failure
	return nil
}

func grpcIdempotencyContext() context.Context {
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	return idempotency.WithContext(ctx, "operation-1")
}

func TestIdempotencyExecutionInterceptorCompletesAndReplays(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := &fakeGRPCIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}}
	interceptor := idempotencyExecutionInterceptor(manager, []string{hellov1.HelloService_Ping_FullMethodName}, logger)
	info := &grpc.UnaryServerInfo{FullMethod: hellov1.HelloService_Ping_FullMethodName}
	request := &hellov1.PingRequest{Message: "hello"}
	response, err := interceptor(grpcIdempotencyContext(), request, info, func(context.Context, any) (any, error) {
		return &hellov1.PingResponse{Message: "hello", Version: "1.0.0"}, nil
	})
	if err != nil || response.(*hellov1.PingResponse).GetMessage() != "hello" {
		t.Fatalf("response=%v error=%v", response, err)
	}
	if manager.fingerprint == "" || manager.completed == nil {
		t.Fatalf("fingerprint=%q completed=%+v", manager.fingerprint, manager.completed)
	}
	encoded, err := json.Marshal(*manager.completed)
	if err != nil {
		t.Fatal(err)
	}
	manager.decision = idempotency.Decision{State: idempotency.StateCompleted, Response: encoded}
	calls := 0
	replayed, err := interceptor(grpcIdempotencyContext(), request, info, func(context.Context, any) (any, error) {
		calls++
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	message, ok := replayed.(proto.Message)
	if !ok || calls != 0 {
		t.Fatalf("replayed=%T calls=%d", replayed, calls)
	}
	field := message.ProtoReflect().Descriptor().Fields().ByName("message")
	if got := message.ProtoReflect().Get(field).String(); got != "hello" {
		t.Fatalf("message = %q", got)
	}
}

func TestGRPCIdempotencyFingerprintIncludesPrincipal(t *testing.T) {
	t.Parallel()
	request := &hellov1.PingRequest{Message: "hello"}
	first, err := grpcIdempotencyFingerprint(grpcIdempotencyContext(), hellov1.HelloService_Ping_FullMethodName, request)
	if err != nil {
		t.Fatal(err)
	}
	other := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-2", Type: platformprincipal.TypeUser})
	second, err := grpcIdempotencyFingerprint(other, hellov1.HelloService_Ping_FullMethodName, request)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("different principals produced the same fingerprint")
	}
}

func TestIdempotencyExecutionInterceptorBypassesUnconfiguredMethod(t *testing.T) {
	t.Parallel()
	manager := &fakeGRPCIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateConflict}}
	interceptor := idempotencyExecutionInterceptor(manager, []string{"/other.Service/Create"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	calls := 0
	response, err := interceptor(
		grpcIdempotencyContext(),
		&hellov1.PingRequest{Message: "hello"},
		&grpc.UnaryServerInfo{FullMethod: hellov1.HelloService_Ping_FullMethodName},
		func(context.Context, any) (any, error) { calls++; return &hellov1.PingResponse{Message: "hello"}, nil },
	)
	if err != nil || calls != 1 || response.(*hellov1.PingResponse).GetMessage() != "hello" || manager.fingerprint != "" {
		t.Fatalf("response=%v error=%v calls=%d fingerprint=%q", response, err, calls, manager.fingerprint)
	}
}

func TestHelloServer_PingThroughGRPC(t *testing.T) {
	t.Parallel()
	authService := auth.New(config.Config{JWT: config.JWT{Issuer: "test", Secret: "01234567890123456789012345678901", TTL: time.Hour}, Auth: config.Auth{ClientID: "client", ClientSecret: "secret"}})
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

func TestAuthenticateGRPC_PSKWildcard(t *testing.T) {
	t.Parallel()
	const key = "01234567890123456789012345678901"
	authService := auth.New(config.Config{JWT: config.JWT{Issuer: "test", Secret: key, TTL: time.Hour}})
	cfg := config.Auth{
		SkipGRPCMethods: []string{"/hello.v1.UserService/*"},
		PSK:             config.PSK{Enabled: true, Key: key, GRPCMethods: []string{"/hello.v1.UserService/*"}},
	}
	for _, test := range []struct {
		name   string
		header string
		code   codes.Code
	}{
		{name: "valid", header: "PSK " + key, code: codes.OK},
		{name: "PSK precedes skip", code: codes.Unauthenticated},
		{name: "bearer rejected", header: "Bearer " + key, code: codes.Unauthenticated},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", test.header))
			authenticated, err := authenticateGRPC(ctx, "/hello.v1.UserService/GetUser", authService, cfg)
			if got := status.Code(err); got != test.code {
				t.Fatalf("status code = %s, want %s", got, test.code)
			}
			if test.code == codes.OK {
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
	const key = "01234567890123456789012345678901"
	service := auth.New(config.Config{JWT: config.JWT{Issuer: "test", Secret: key, TTL: time.Hour}, Auth: config.Auth{ClientID: "client", ClientSecret: "secret"}})
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

func TestHelloRequirementProtectsBusinessRPC(t *testing.T) {
	t.Parallel()
	requirement, ok := helloRequirement(true)(hellov1.HelloService_Ping_FullMethodName)
	if !ok || requirement.Resource != "example.hello" || requirement.Action != "ping" || requirement.Scope != platformauthz.ScopePrincipal {
		t.Fatalf("requirement = %+v, %v", requirement, ok)
	}
	if _, ok := helloRequirement(false)(hellov1.HelloService_Ping_FullMethodName); ok {
		t.Fatal("disabled authorization must not enforce")
	}
}
