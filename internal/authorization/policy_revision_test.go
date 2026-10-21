package authorization

import (
	"context"
	"errors"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

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
	schema, err := datapermission.NewSchema("tenant.member", map[string]datapermission.Field{"owner_id": {Column: "tm.user_id", Type: datapermission.ValueTypeText}})
	require.NoError(t, err)
	schemas, err := datapermission.NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	engine, err := datapermission.NewRuntimeEngine(schemas, resources)
	require.NoError(t, err)
	data := datapermission.NewRuntimeLoader(nil, engine)
	grants := &revisionAuthorizerStub{grants: "grant-1"}
	principal := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"}
	revisions, err := NewPolicyRevisions(data, grants)
	require.NoError(t, err)

	beforeCtx, revision, err := revisions.ForRequest(t.Context(), principal)
	require.NoError(t, err)
	require.Len(t, revision, 32)
	_, again, err := revisions.ForRequest(t.Context(), principal)
	require.NoError(t, err)
	require.Equal(t, revision, again)

	require.NoError(t, engine.Replace([]datapermission.Policy{{
		APIVersion: datapermission.PolicyAPIVersion, Kind: datapermission.PolicyKind,
		Metadata: datapermission.PolicyMetadata{Code: "member-own", Name: "Member own"},
		Scope:    datapermission.PolicyBoundary{Type: datapermission.PolicyScopeTenant, TenantID: "tenant-1"},
		Spec:     datapermission.PolicySpec{Resource: "tenant.member", Actions: []string{"read"}, Condition: "resource.owner_id == subject.id", Effect: datapermission.EffectAllow},
	}}))
	afterCtx, changed, err := revisions.ForRequest(t.Context(), principal)
	require.NoError(t, err)
	require.NotEqual(t, revision, changed)
	subject := resourceaction.Subject{ID: principal.ID, Type: "user", Authenticated: true, TenantID: principal.TenantID}
	row := datapermission.ResourceAttributes{"owner_id": principal.ID}
	oldAllowed, err := engine.EvaluateCurrent(beforeCtx, "tenant.member", "read", subject, datapermission.SubjectAttributes{"id": principal.ID}, row)
	require.NoError(t, err)
	require.False(t, oldAllowed)
	newAllowed, err := engine.EvaluateCurrent(afterCtx, "tenant.member", "read", subject, datapermission.SubjectAttributes{"id": principal.ID}, row)
	require.NoError(t, err)
	require.True(t, newAllowed)
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

func TestPolicyRevisionsFailsClosedWithoutDataSnapshot(t *testing.T) {
	t.Parallel()
	revisions, err := NewPolicyRevisions(datapermission.NewRuntimeLoader(nil, nil), &revisionAuthorizerStub{grants: "grant-1"})
	require.NoError(t, err)
	_, _, err = revisions.ForRequest(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
}

func TestPolicyRevisionsRequireSnapshotProviderAtConstruction(t *testing.T) {
	t.Parallel()
	_, err := NewPolicyRevisions(nil, nil)
	require.Error(t, err)
	var typedNil *Authorizer
	_, err = NewPolicyRevisions(nil, typedNil)
	require.Error(t, err)
}
