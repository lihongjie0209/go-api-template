package cache

import (
	"context"
	"testing"
	"time"
)

type contextKey string

func TestAfterCommitContextDetachesCancellationAndRetainsValues(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.WithValue(t.Context(), contextKey("request"), "request-1"))
	cancelParent()

	ctx, cancel := AfterCommitContext(parent)
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("AfterCommitContext() inherited cancellation: %v", err)
	}
	if got := ctx.Value(contextKey("request")); got != "request-1" {
		t.Fatalf("AfterCommitContext() value = %v", got)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > afterCommitTimeout {
		t.Fatalf("AfterCommitContext() deadline = %v, %v", deadline, ok)
	}
}
