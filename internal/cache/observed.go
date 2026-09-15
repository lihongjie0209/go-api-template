package cache

import (
	"context"
	"errors"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

var cacheTracer = otel.Tracer("go-api-template/infrastructure/cache")

type observedStore struct {
	next    Store
	metrics *observability.Metrics
	backend string
}

func ObserveStore(next Store, metrics *observability.Metrics, backend string) Store {
	if next == nil {
		return nil
	}
	if metrics == nil {
		return next
	}
	return &observedStore{next: next, metrics: metrics, backend: backend}
}

func (s *observedStore) Get(ctx context.Context, key string) ([]byte, error) {
	ctx, done := s.start(ctx, "get")
	value, err := s.next.Get(ctx, key)
	status := "success"
	if errors.Is(err, ErrMiss) {
		status = "miss"
	} else if err != nil {
		status = "error"
	}
	done(status, err)
	return value, err
}
func (s *observedStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	ctx, done := s.start(ctx, "set")
	err := s.next.Set(ctx, key, value, ttl)
	done(errorStatus(err), err)
	return err
}
func (s *observedStore) SetIfAbsent(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	ctx, done := s.start(ctx, "set_if_absent")
	created, err := s.next.SetIfAbsent(ctx, key, value, ttl)
	status := errorStatus(err)
	if err == nil && !created {
		status = "exists"
	}
	done(status, err)
	return created, err
}
func (s *observedStore) Delete(ctx context.Context, keys ...string) error {
	ctx, done := s.start(ctx, "delete")
	err := s.next.Delete(ctx, keys...)
	done(errorStatus(err), err)
	return err
}
func (s *observedStore) Exists(ctx context.Context, key string) (bool, error) {
	ctx, done := s.start(ctx, "exists")
	exists, err := s.next.Exists(ctx, key)
	done(errorStatus(err), err)
	return exists, err
}
func (s *observedStore) start(ctx context.Context, operation string) (context.Context, func(string, error)) {
	started := time.Now()
	ctx, span := cacheTracer.Start(ctx, "cache."+operation)
	span.SetAttributes(attribute.String("cache.backend", s.backend), attribute.String("cache.operation", operation))
	return ctx, func(status string, err error) {
		if err != nil && !errors.Is(err, ErrMiss) {
			span.RecordError(err)
			span.SetStatus(codes.Error, "operation failed")
		}
		span.SetAttributes(attribute.String("cache.status", status))
		span.End()
		s.metrics.ObserveInfrastructure("cache", s.backend, operation, status, started)
	}
}

type observedLocker struct {
	next    Locker
	metrics *observability.Metrics
	backend string
}

func ObserveLocker(next Locker, metrics *observability.Metrics, backend string) Locker {
	if next == nil {
		return nil
	}
	if metrics == nil {
		return next
	}
	return &observedLocker{next: next, metrics: metrics, backend: backend}
}
func (l *observedLocker) TryLock(ctx context.Context, key string, ttl time.Duration) (Lock, bool, error) {
	ctx, done := l.start(ctx, "try_lock")
	lock, acquired, err := l.next.TryLock(ctx, key, ttl)
	status := errorStatus(err)
	if err == nil && !acquired {
		status = "contended"
	}
	done(status, err)
	if lock != nil {
		lock = &observedLock{next: lock, parent: l}
	}
	return lock, acquired, err
}
func (l *observedLocker) Lock(ctx context.Context, key string, ttl, retryDelay time.Duration) (Lock, error) {
	ctx, done := l.start(ctx, "lock")
	lock, err := l.next.Lock(ctx, key, ttl, retryDelay)
	done(errorStatus(err), err)
	if lock != nil {
		lock = &observedLock{next: lock, parent: l}
	}
	return lock, err
}
func (l *observedLocker) start(ctx context.Context, operation string) (context.Context, func(string, error)) {
	started := time.Now()
	ctx, span := cacheTracer.Start(ctx, "lock."+operation)
	span.SetAttributes(attribute.String("lock.backend", l.backend), attribute.String("lock.operation", operation))
	return ctx, func(status string, err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "operation failed")
		}
		span.SetAttributes(attribute.String("lock.status", status))
		span.End()
		l.metrics.ObserveInfrastructure("lock", l.backend, operation, status, started)
	}
}

type observedLock struct {
	next   Lock
	parent *observedLocker
}

func (l *observedLock) Extend(ctx context.Context) error {
	ctx, done := l.parent.start(ctx, "extend")
	err := l.next.Extend(ctx)
	done(errorStatus(err), err)
	return err
}
func (l *observedLock) Unlock(ctx context.Context) error {
	ctx, done := l.parent.start(ctx, "unlock")
	err := l.next.Unlock(ctx)
	done(errorStatus(err), err)
	return err
}
func (l *observedLock) Until() time.Time { return l.next.Until() }

func errorStatus(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}

var _ Store = (*observedStore)(nil)
var _ Locker = (*observedLocker)(nil)
