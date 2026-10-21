package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	"go.opentelemetry.io/otel/trace"
	"gopkg.in/natefinch/lumberjack.v2"
)

func New(cfg config.Log) (*slog.Logger, io.Closer, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.File), 0o750); err != nil {
		return nil, nil, fmt.Errorf("create log directory: %w", err)
	}
	rotator := &lumberjack.Logger{Filename: cfg.File, MaxSize: cfg.MaxSizeMB, MaxBackups: cfg.MaxBackups, MaxAge: cfg.MaxAgeDays, Compress: cfg.Compress}
	writer := io.MultiWriter(os.Stdout, rotator)
	level := new(slog.LevelVar)
	if err := level.UnmarshalText([]byte(strings.ToLower(cfg.Level))); err != nil {
		return nil, nil, fmt.Errorf("parse log level: %w", err)
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.Format == "text" {
		handler = slog.NewTextHandler(writer, opts)
	} else {
		handler = slog.NewJSONHandler(writer, opts)
	}
	return slog.New(contextHandler{next: handler}), rotator, nil
}

type contextHandler struct {
	next      slog.Handler
	boundKeys map[string]struct{}
}

func (h contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h contextHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		clean.AddAttrs(sanitizeLogAttr(attr))
		return true
	})
	record = clean
	if id, ok := requestid.FromContext(ctx); ok && !h.hasKey(record, "request_id") {
		record.AddAttrs(slog.String("request_id", id))
	}
	if span := trace.SpanContextFromContext(ctx); span.IsValid() {
		if !h.hasKey(record, "trace_id") {
			record.AddAttrs(slog.String("trace_id", span.TraceID().String()))
		}
		if !h.hasKey(record, "span_id") {
			record.AddAttrs(slog.String("span_id", span.SpanID().String()))
		}
	}
	return h.next.Handle(ctx, record)
}

func sanitizeLogAttr(attr slog.Attr) slog.Attr {
	if attr.Key == "panic" {
		return slog.String(attr.Key, "handler_panic")
	}
	value := attr.Value.Resolve()
	if attr.Key == "error" || attr.Key == "err" {
		if err, ok := value.Any().(error); ok {
			if reason, safe := observability.SafeReason(err); safe {
				return slog.String(attr.Key, reason)
			}
		}
		return slog.String(attr.Key, "operation_failed")
	}
	if value.Kind() == slog.KindGroup {
		attrs := append([]slog.Attr(nil), value.Group()...)
		for index := range attrs {
			attrs[index] = sanitizeLogAttr(attrs[index])
		}
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(attrs...)}
	}
	if err, ok := value.Any().(error); ok {
		if reason, safe := observability.SafeReason(err); safe {
			return slog.String(attr.Key, reason)
		}
		return slog.String(attr.Key, "operation_failed")
	}
	return slog.Attr{Key: attr.Key, Value: value}
}

func recordHasKey(record slog.Record, key string) bool {
	found := false
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == key {
			found = true
			return false
		}
		return true
	})
	return found
}

func (h contextHandler) hasKey(record slog.Record, key string) bool {
	_, bound := h.boundKeys[key]
	return bound || recordHasKey(record, key)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	keys := make(map[string]struct{}, len(h.boundKeys)+len(attrs))
	for key := range h.boundKeys {
		keys[key] = struct{}{}
	}
	clean := make([]slog.Attr, len(attrs))
	for index, attr := range attrs {
		keys[attr.Key] = struct{}{}
		clean[index] = sanitizeLogAttr(attr)
	}
	return contextHandler{next: h.next.WithAttrs(clean), boundKeys: keys}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{next: h.next.WithGroup(name), boundKeys: h.boundKeys}
}
