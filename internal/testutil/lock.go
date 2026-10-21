package testutil

import (
	"context"
	"time"

	"github.com/lihongjie0209/microservice-platform-go/distlock"
)

// LockStub lets service unit tests exercise acquisition and release failures
// without Redis or a second business service.
type LockStub struct {
	AcquireErr error
	ReleaseErr error
}

func (stub LockStub) TryLock(context.Context, string, time.Duration) (distlock.Mutex, bool, error) {
	if stub.AcquireErr != nil {
		return nil, false, stub.AcquireErr
	}
	return stubMutex{releaseErr: stub.ReleaseErr}, true, nil
}

func (stub LockStub) Lock(context.Context, string, time.Duration, time.Duration) (distlock.Mutex, error) {
	if stub.AcquireErr != nil {
		return nil, stub.AcquireErr
	}
	return stubMutex{releaseErr: stub.ReleaseErr}, nil
}

type stubMutex struct{ releaseErr error }

func (stubMutex) Extend(context.Context) error       { return nil }
func (mutex stubMutex) Unlock(context.Context) error { return mutex.releaseErr }
func (stubMutex) Until() time.Time                   { return time.Now().Add(time.Minute) }
