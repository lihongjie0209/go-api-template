package background

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkerStopCancelsAndJoins(t *testing.T) {
	started := make(chan struct{})
	returned := make(chan struct{})
	worker := New(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(returned)
	})

	worker.Start()
	worker.Start()
	<-started
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	select {
	case <-returned:
	default:
		t.Fatal("Stop() returned before the worker")
	}
}

func TestWorkerStopHonorsDeadline(t *testing.T) {
	release := make(chan struct{})
	worker := New(func(context.Context) { <-release })
	worker.Start()

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := worker.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() error = %v, want deadline exceeded", err)
	}
	close(release)
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
}

func TestWorkerStopBeforeStart(t *testing.T) {
	worker := New(func(context.Context) { t.Fatal("worker unexpectedly ran") })
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}
