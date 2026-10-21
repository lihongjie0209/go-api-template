package grpctransport

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGRPCRecoveryDoesNotLogPanicDetails(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		run  func(*slog.Logger) error
	}{
		{
			name: "unary",
			run: func(logger *slog.Logger) error {
				_, err := recoveryInterceptor(logger)(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/test.Service/Call"}, func(_ context.Context, _ any) (any, error) {
					panic(errors.New("credential=secret-value"))
				})
				return err
			},
		},
		{
			name: "stream",
			run: func(logger *slog.Logger) error {
				return recoveryStreamInterceptor(logger)(nil, &contextServerStream{ctx: t.Context()}, &grpc.StreamServerInfo{FullMethod: "/test.Service/Watch"}, func(any, grpc.ServerStream) error {
					panic(errors.New("credential=secret-value"))
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			err := test.run(slog.New(slog.NewJSONHandler(&output, nil)))
			if status.Code(err) != codes.Internal {
				t.Fatalf("status = %v, want Internal", err)
			}
			if strings.Contains(output.String(), "secret-value") || !strings.Contains(output.String(), "grpc_handler_panic") {
				t.Fatalf("gRPC recovery leaked panic details or omitted safe code: %s", output.String())
			}
		})
	}
}
