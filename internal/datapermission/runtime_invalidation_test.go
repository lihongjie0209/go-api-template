package datapermission

import (
	"context"
	"errors"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/stretchr/testify/require"
)

type failingChangeSync struct {
	err       error
	onFailure func()
}

func (s *failingChangeSync) Refresh(context.Context) error { return nil }
func (s *failingChangeSync) Changed(context.Context) error {
	if s.onFailure != nil {
		s.onFailure()
	}
	return s.err
}
func (s *failingChangeSync) Run(context.Context)              {}
func (s *failingChangeSync) SetFailureHandler(handler func()) { s.onFailure = handler }

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
