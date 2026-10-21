package httptransport

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
)

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
