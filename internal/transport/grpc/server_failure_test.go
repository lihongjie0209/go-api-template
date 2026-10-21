package grpctransport

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
)

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
