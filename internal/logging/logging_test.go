package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

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
