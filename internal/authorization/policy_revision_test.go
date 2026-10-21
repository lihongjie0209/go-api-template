package authorization

import (
	"context"
	"errors"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type revisionSyncStub struct{ revision string }

func (revisionSyncStub) Refresh(context.Context) error { return nil }
func (revisionSyncStub) Changed(context.Context) error { return nil }
func (revisionSyncStub) Run(context.Context)           {}
func (s revisionSyncStub) Revision() string            { return s.revision }

type revisionAuthorizerStub struct {
	grants string
	err    error
}

func (*revisionAuthorizerStub) Authorize(context.Context, platformprincipal.Principal, platformauthz.Requirement) error {
	return nil
}

func (s *revisionAuthorizerStub) Snapshot(ctx context.Context, _ platformprincipal.Principal) (context.Context, string, error) {
	return ctx, s.grants, s.err
}

func TestPolicyRevisionsTracksDataSnapshot(t *testing.T) {
	t.Parallel()
	data := datapermission.NewRuntimeLoader(nil, nil)
	data.ConfigureSync(revisionSyncStub{revision: "data-1"})
	grants := &revisionAuthorizerStub{grants: "grant-1"}
	principal := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}
	revisions, err := NewPolicyRevisions(data, grants)
	require.NoError(t, err)

	_, revision, err := revisions.ForRequest(t.Context(), principal)
	require.NoError(t, err)
	require.Len(t, revision, 32)
	_, again, err := revisions.ForRequest(t.Context(), principal)
	require.NoError(t, err)
	require.Equal(t, revision, again)

	data.ConfigureSync(revisionSyncStub{revision: "data-2"})
	_, changed, err := revisions.ForRequest(t.Context(), principal)
	require.NoError(t, err)
	require.NotEqual(t, revision, changed)
	grants.grants = "grant-2"
	_, changedAgain, err := revisions.ForRequest(t.Context(), principal)
	require.NoError(t, err)
	require.NotEqual(t, changed, changedAgain)
}

func TestPolicyRevisionsFailsClosedWhenGrantsAreUnavailable(t *testing.T) {
	t.Parallel()
	grants := &revisionAuthorizerStub{err: errors.New("database unavailable")}
	revisions, err := NewPolicyRevisions(nil, grants)
	require.NoError(t, err)
	_, _, err = revisions.ForRequest(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
}

func TestPolicyRevisionsRequireSnapshotProviderAtConstruction(t *testing.T) {
	t.Parallel()
	_, err := NewPolicyRevisions(nil, nil)
	require.Error(t, err)
}
