package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	"go.opentelemetry.io/otel/trace"
)

func TestContextHandlerAddsRequestAndTraceCorrelation(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(contextHandler{next: slog.NewJSONHandler(&output, nil)}).With("service", "orders-service")
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3},
		SpanID:  trace.SpanID{4, 5, 6},
	})
	ctx := trace.ContextWithSpanContext(requestid.WithContext(context.Background(), "request-1"), spanContext)
	logger.InfoContext(ctx, "handled")
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["request_id"] != "request-1" || record["trace_id"] != spanContext.TraceID().String() || record["span_id"] != spanContext.SpanID().String() || record["service"] != "orders-service" {
		t.Fatalf("record=%v", record)
	}
}

func TestContextHandlerOmitsAbsentCorrelation(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(contextHandler{next: slog.NewJSONHandler(&output, nil)})
	logger.InfoContext(context.Background(), "startup")
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if _, ok := record["request_id"]; ok {
		t.Fatalf("record unexpectedly contains request_id: %v", record)
	}
}

func TestContextHandlerDoesNotDuplicateBoundCorrelation(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(contextHandler{next: slog.NewJSONHandler(&output, nil)}).
		With("request_id", "explicit-request", "trace_id", "explicit-trace", "span_id", "explicit-span")
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})
	ctx := trace.ContextWithSpanContext(requestid.WithContext(context.Background(), "context-request"), spanContext)
	logger.InfoContext(ctx, "handled")

	line := output.String()
	for _, key := range []string{"request_id", "trace_id", "span_id"} {
		if count := bytes.Count([]byte(line), []byte(`"`+key+`"`)); count != 1 {
			t.Fatalf("%s occurs %d times in %s", key, count, line)
		}
	}
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["request_id"] != "explicit-request" || record["trace_id"] != "explicit-trace" || record["span_id"] != "explicit-span" {
		t.Fatalf("record=%v", record)
	}
}

func TestNewWritesStructuredFileAndClosesRotator(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "app.log")
	logger, closer, err := New(config.Log{
		Level: "info", Format: "json", File: path,
		MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("ready", "service", "test")
	if err := closer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode log %q: %v", data, err)
	}
	if record["msg"] != "ready" || record["service"] != "test" || record["level"] != "INFO" {
		t.Fatalf("record=%v", record)
	}
}
