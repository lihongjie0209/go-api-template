package objectstorage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

var storageTracer = otel.Tracer("go-api-template/infrastructure/objectstorage")

type observedStore struct {
	next    Store
	metrics *observability.Metrics
	backend string
}

func Observe(next Store, metrics *observability.Metrics, backend string) Store {
	if next == nil {
		return nil
	}
	if metrics == nil {
		return next
	}
	return &observedStore{next: next, metrics: metrics, backend: backend}
}

func (s *observedStore) Put(ctx context.Context, input PutInput) (Info, error) {
	ctx, done := s.start(ctx, "put")
	info, err := s.next.Put(ctx, input)
	done(telemetryError(err))
	return info, err
}
func (s *observedStore) Get(ctx context.Context, key string) (*Object, error) {
	ctx, done := s.start(ctx, "get")
	object, err := s.next.Get(ctx, key)
	if err != nil {
		done(telemetryError(err))
		return nil, err
	}
	if object == nil || object.Body == nil {
		err = errors.New("object storage returned an empty body")
		done(err)
		return nil, err
	}
	object.Body = &observedReadCloser{ReadCloser: object.Body, done: func(err error) { done(telemetryError(err)) }}
	return object, err
}
func (s *observedStore) Stat(ctx context.Context, key string) (Info, error) {
	ctx, done := s.start(ctx, "stat")
	info, err := s.next.Stat(ctx, key)
	done(telemetryError(err))
	return info, err
}
func (s *observedStore) Delete(ctx context.Context, key string) error {
	ctx, done := s.start(ctx, "delete")
	err := s.next.Delete(ctx, key)
	done(telemetryError(err))
	return err
}
func (s *observedStore) Presign(ctx context.Context, key string, operation Operation, ttl time.Duration) (SignedURL, error) {
	ctx, done := s.start(ctx, "presign_"+string(operation))
	result, err := s.next.Presign(ctx, key, operation, ttl)
	done(telemetryError(err))
	return result, err
}

func telemetryError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New("object storage operation failed")
}
func (s *observedStore) start(ctx context.Context, operation string) (context.Context, func(error)) {
	started := time.Now()
	ctx, span := storageTracer.Start(ctx, "object_storage."+operation)
	span.SetAttributes(attribute.String("object_storage.backend", s.backend), attribute.String("object_storage.operation", operation))
	return ctx, func(err error) {
		status := "success"
		if err != nil {
			status = "error"
			span.RecordError(err)
			span.SetStatus(codes.Error, "operation failed")
		}
		span.SetAttributes(attribute.String("object_storage.status", status))
		span.End()
		s.metrics.ObserveInfrastructure("object_storage", s.backend, operation, status, started)
	}
}

var _ Store = (*observedStore)(nil)

type observedReadCloser struct {
	io.ReadCloser
	done func(error)
	once sync.Once
}

func (r *observedReadCloser) Read(buffer []byte) (int, error) {
	count, err := r.ReadCloser.Read(buffer)
	if errors.Is(err, io.EOF) {
		r.finish(nil)
	} else if err != nil {
		r.finish(fmt.Errorf("read object body: %w", err))
	}
	return count, err
}

func (r *observedReadCloser) Close() error {
	err := r.ReadCloser.Close()
	if err != nil {
		r.finish(fmt.Errorf("close object body: %w", err))
	} else {
		r.finish(nil)
	}
	return err
}

func (r *observedReadCloser) finish(err error) { r.once.Do(func() { r.done(err) }) }
