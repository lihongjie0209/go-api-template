package authorization

import (
	"context"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/stretchr/testify/require"
)

type revisionSyncStub struct{ revision string }

func (revisionSyncStub) Refresh(context.Context) error { return nil }
func (revisionSyncStub) Changed(context.Context) error { return nil }
func (revisionSyncStub) Run(context.Context)           {}
func (s revisionSyncStub) Revision() string            { return s.revision }

func TestPolicyRevisionsTracksDataSnapshot(t *testing.T) {
	t.Parallel()
	data := datapermission.NewRuntimeLoader(nil, nil)
	data.ConfigureSync(revisionSyncStub{revision: "data-1"})

	revision := NewPolicyRevisions(data).Revision()
	require.Len(t, revision, 32)
	require.Equal(t, revision, NewPolicyRevisions(data).Revision())

	data.ConfigureSync(revisionSyncStub{revision: "data-2"})
	require.NotEqual(t, revision, NewPolicyRevisions(data).Revision())
}
