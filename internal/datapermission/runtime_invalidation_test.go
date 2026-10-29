package datapermission

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/stretchr/testify/require"
)

type failingChangeSync struct {
	err       error
	onFailure func()
	changed   func(context.Context) error
	refresh   func(context.Context) error
}

func (s *failingChangeSync) Refresh(ctx context.Context) error {
	if s.refresh != nil {
		return s.refresh(ctx)
	}
	return nil
}
func (s *failingChangeSync) Changed(ctx context.Context) error {
	if s.onFailure != nil {
		s.onFailure()
	}
	if s.changed != nil {
		return s.changed(ctx)
	}
	return s.err
}
func (s *failingChangeSync) Run(context.Context)              {}
func (s *failingChangeSync) SetFailureHandler(handler func()) { s.onFailure = handler }
func (s *failingChangeSync) Timeout() time.Duration           { return time.Second }

func TestRuntimeChangeFailureInvalidatesLocalDataPermissions(t *testing.T) {
	t.Parallel()
	schema, err := NewSchema("tenant.member", map[string]Field{"owner_id": {Column: "tm.user_id", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	engine, err := NewRuntimeEngine(schemas, resources)
	require.NoError(t, err)
	loader := NewRuntimeLoader(nil, engine)
	want := errors.New("policy reload unavailable")
	loader.ConfigureSync(&failingChangeSync{err: want})
	require.ErrorIs(t, loader.Changed(t.Context()), want)
	_, _, err = loader.Pin(t.Context())
	require.ErrorIs(t, err, ErrSnapshotUnavailable)
	require.NoError(t, engine.Replace(nil))
	_, _, err = loader.Pin(t.Context())
	require.NoError(t, err)
}

func TestDirectRuntimeRefreshFailureInvalidatesLocalDataPermissions(t *testing.T) {
	t.Parallel()
	service, mock := dataLifecycleFixture(t, true)
	loader := service.runtime
	require.NoError(t, loader.engine.Replace(nil))
	_, _, err := loader.Pin(t.Context())
	require.NoError(t, err)
	want := errors.New("policy database unavailable")
	mock.ExpectQuery("SELECT p.code,p.scope").WillReturnError(want)
	require.ErrorIs(t, loader.Refresh(t.Context()), want)
	_, _, err = loader.Pin(t.Context())
	require.ErrorIs(t, err, ErrSnapshotUnavailable)
	mock.ExpectQuery("SELECT p.code,p.scope").WillReturnRows(sqlmock.NewRows([]string{"code", "scope", "tenant_id", "document"}))
	require.NoError(t, loader.Refresh(t.Context()))
	_, _, err = loader.Pin(t.Context())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDirectRuntimeCompileFailureInvalidatesLocalDataPermissions(t *testing.T) {
	t.Parallel()
	service, mock := dataLifecycleFixture(t, true)
	loader := service.runtime
	require.NoError(t, loader.engine.Replace(nil))
	policy := dataPolicyFixture()
	policy.Scope = PolicyBoundary{Type: PolicyScopeGlobal}
	policy.Spec.Resource = "unknown.resource"
	document, err := MarshalPolicy(policy)
	require.NoError(t, err)
	mock.ExpectQuery("SELECT p.code,p.scope").WillReturnRows(sqlmock.NewRows([]string{"code", "scope", "tenant_id", "document"}).
		AddRow(policy.Metadata.Code, string(policy.Scope.Type), nil, string(document)))
	require.Error(t, loader.Refresh(t.Context()))
	_, _, err = loader.Pin(t.Context())
	require.ErrorIs(t, err, ErrSnapshotUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRuntimeRefreshUsesConfiguredSync(t *testing.T) {
	t.Parallel()
	want := errors.New("sync refresh unavailable")
	called := false
	loader := NewRuntimeLoader(nil, nil)
	loader.ConfigureSync(&failingChangeSync{refresh: func(context.Context) error {
		called = true
		return want
	}})
	require.ErrorIs(t, loader.Refresh(t.Context()), want)
	require.True(t, called)
}

type concurrentRefreshRepository struct {
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
	want    error
}

func (r *concurrentRefreshRepository) LoadAllPublished(context.Context) ([]Policy, error) {
	if r.calls.Add(1) == 1 {
		close(r.started)
		<-r.release
		return nil, r.want
	}
	return nil, nil
}

func (*concurrentRefreshRepository) LoadPublishedSnapshot(context.Context) ([]Policy, string, error) {
	return nil, "", nil
}

func TestDirectRefreshFailureCannotInvalidateLaterSuccess(t *testing.T) {
	t.Parallel()
	service, _ := dataLifecycleFixture(t, true)
	loader := service.runtime
	require.NoError(t, loader.engine.Replace(nil))
	want := errors.New("old refresh failed")
	repository := &concurrentRefreshRepository{started: make(chan struct{}), release: make(chan struct{}), want: want}
	loader.repository = repository
	first := make(chan error, 1)
	second := make(chan error, 1)
	secondFinished := make(chan struct{})
	go func() { first <- loader.Refresh(t.Context()) }()
	<-repository.started
	go func() {
		second <- loader.Refresh(t.Context())
		close(secondFinished)
	}()
	go func() {
		select {
		case <-secondFinished:
		case <-time.After(100 * time.Millisecond):
		}
		close(repository.release)
	}()
	require.ErrorIs(t, <-first, want)
	require.NoError(t, <-second)
	_, _, err := loader.Pin(t.Context())
	require.NoError(t, err)
}

type postCommitContextKey struct{}

func TestPostCommitPolicySyncKeepsTraceValuesAfterCallerCancellation(t *testing.T) {
	t.Parallel()
	requestCtx, cancelRequest := context.WithCancel(context.WithValue(t.Context(), postCommitContextKey{}, "request-1"))
	cancelRequest()
	syncer := &failingChangeSync{changed: func(ctx context.Context) error {
		require.NoError(t, ctx.Err())
		require.Equal(t, "request-1", ctx.Value(postCommitContextKey{}))
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.WithinDuration(t, time.Now().Add(6*time.Second), deadline, time.Second)
		return nil
	}}
	loader := NewRuntimeLoader(nil, nil)
	loader.ConfigureSync(syncer)
	service := &LifecycleService{runtime: loader}
	postCtx, stop := service.postCommitContext(requestCtx)
	defer stop()
	require.NoError(t, loader.Changed(postCtx))
}
