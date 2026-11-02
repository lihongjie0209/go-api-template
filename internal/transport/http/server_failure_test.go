package httptransport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPStartupFailureDoesNotExposeListenAddress(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := startHTTPServer(context.Background(), &http.Server{Addr: "secret-value"}, logger)
	if !errors.Is(err, errHTTPListenFailed) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("startHTTPServer() = %v; want safe listen failure", err)
	}
}

func TestHTTPStartupHonorsCanceledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := startHTTPServer(ctx, &http.Server{Addr: "127.0.0.1:0"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("startHTTPServer() = %v; want context cancellation", err)
	}
}

type failingHTTPListener struct{}

func (failingHTTPListener) Accept() (net.Conn, error) {
	return nil, errors.New("listener credential=secret-value")
}
func (failingHTTPListener) Close() error   { return nil }
func (failingHTTPListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestHTTPServerFailureDoesNotLogListenerDetails(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	serveHTTP(&http.Server{}, failingHTTPListener{}, logger)
	if strings.Contains(output.String(), "secret-value") || !strings.Contains(output.String(), "http_server_stopped") {
		t.Fatalf("HTTP server log leaked listener details or omitted safe code: %s", output.String())
	}
}
