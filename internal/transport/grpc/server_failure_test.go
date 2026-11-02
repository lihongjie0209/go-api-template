package grpctransport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
)

func TestGRPCStartupFailureDoesNotExposeListenAddress(t *testing.T) {
	t.Parallel()
	server := &Server{address: "secret-value", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	err := server.start(true)(context.Background())
	if !errors.Is(err, errGRPCListenFailed) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("start() = %v; want safe listen failure", err)
	}
}

func TestGRPCStartupHonorsCanceledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := &Server{address: "127.0.0.1:0", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	err := server.start(true)(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("start() = %v; want context cancellation", err)
	}
}

type failingGRPCListener struct{}

func (failingGRPCListener) Accept() (net.Conn, error) {
	return nil, errors.New("listener credential=secret-value")
}
func (failingGRPCListener) Close() error   { return nil }
func (failingGRPCListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestGRPCServerFailureDoesNotLogListenerDetails(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	server := &Server{server: grpc.NewServer(), logger: slog.New(slog.NewJSONHandler(&output, nil))}
	server.serve(failingGRPCListener{})
	if strings.Contains(output.String(), "secret-value") || !strings.Contains(output.String(), "grpc_server_stopped") {
		t.Fatalf("gRPC server log leaked listener details or omitted safe code: %s", output.String())
	}
}
