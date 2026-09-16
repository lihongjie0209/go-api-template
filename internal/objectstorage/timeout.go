package objectstorage

import (
	"context"
	"errors"
	"io"
	"time"
)

type timeoutStore struct {
	next    Store
	timeout time.Duration
}

// WithTimeout gives every object-store request an upper bound while retaining
// an earlier caller deadline. Get keeps the derived context alive until the
// returned body is closed so streaming reads remain cancellable.
func WithTimeout(next Store, timeout time.Duration) Store {
	if next == nil || timeout <= 0 {
		return next
	}
	return &timeoutStore{next: next, timeout: timeout}
}

func (s *timeoutStore) Put(ctx context.Context, input PutInput) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.next.Put(ctx, input)
}

func (s *timeoutStore) Get(ctx context.Context, key string) (*Object, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	object, err := s.next.Get(ctx, key)
	if err != nil {
		cancel()
		return nil, err
	}
	if object == nil || object.Body == nil {
		cancel()
		return nil, errors.New("object storage returned an empty body")
	}
	object.Body = &cancelReadCloser{ReadCloser: object.Body, cancel: cancel}
	return object, nil
}

func (s *timeoutStore) Stat(ctx context.Context, key string) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.next.Stat(ctx, key)
}

func (s *timeoutStore) Delete(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.next.Delete(ctx, key)
}

func (s *timeoutStore) Presign(ctx context.Context, key string, operation Operation, ttl time.Duration) (SignedURL, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.next.Presign(ctx, key, operation, ttl)
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}

var _ Store = (*timeoutStore)(nil)
