// Package background manages long-running goroutines that are owned by the
// application lifecycle.
package background

import (
	"context"
	"sync"
)

// Worker runs one context-aware function and joins it during shutdown.
// A Worker is single-use: repeated Start calls are ignored.
type Worker struct {
	run func(context.Context)

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func New(run func(context.Context)) *Worker {
	return &Worker{run: run}
}

func (w *Worker) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	done := w.done
	go func() {
		defer close(done)
		w.run(ctx)
	}()
}

// Stop cancels the worker and waits for it to return or for ctx to expire.
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	cancel := w.cancel
	done := w.done
	w.mu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
