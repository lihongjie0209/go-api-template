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
	grants        string
	err           error
	afterSnapshot func()
}

func (*revisionAuthorizerStub) Authorize(context.Context, platformprincipal.Principal, platformauthz.Requirement) error {
	return nil
}

func (s *revisionAuthorizerStub) Snapshot(ctx context.Context, _ platformprincipal.Principal) (context.Context, string, error) {
	if s.afterSnapshot != nil {
		s.afterSnapshot()
	}
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

func TestPolicyRevisionsFailsClosedWithoutGrantRevision(t *testing.T) {
	revisions, err := NewPolicyRevisions(nil, &revisionAuthorizerStub{})
	require.NoError(t, err)
	_, revision, err := revisions.ForRequest(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
	require.Empty(t, revision)
}

func TestPolicyRevisionsFailsClosedWithoutDataSnapshot(t *testing.T) {
	t.Parallel()
	revisions, err := NewPolicyRevisions(datapermission.NewRuntimeLoader(nil, nil), &revisionAuthorizerStub{grants: "grant-1"})
	require.NoError(t, err)
	_, _, err = revisions.ForRequest(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
}

func TestPolicyRevisionsCanceledContextFailsClosed(t *testing.T) {
	revisions, err := NewPolicyRevisions(nil, &revisionAuthorizerStub{grants: "grant-1"})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, revision, err := revisions.ForRequest(ctx, platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
	require.Empty(t, revision)
}

func TestPolicyRevisionsCancellationDuringGrantReadFailsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	revisions, err := NewPolicyRevisions(nil, &revisionAuthorizerStub{grants: "grant-1", afterSnapshot: cancel})
	require.NoError(t, err)
	_, revision, err := revisions.ForRequest(ctx, platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
	require.Empty(t, revision)
}

func TestPolicyRevisionsFailureInvalidatesPriorGrantContext(t *testing.T) {
	principal := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}
	priorCtx := context.WithValue(t.Context(), grantSnapshotKey{}, grantSnapshot{
		principal: principal,
		platform:  map[string]struct{}{"identity.user\x00read": {}},
	})
	a, mock := testAuthorizer(t)
	requirement := platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform}
	require.NoError(t, a.Authorize(priorCtx, principal, requirement))
	for _, test := range []struct {
		name      string
		data      *datapermission.RuntimeLoader
		authorize *revisionAuthorizerStub
	}{
		{name: "grant read", authorize: &revisionAuthorizerStub{err: errors.New("database unavailable")}},
		{name: "data snapshot", data: datapermission.NewRuntimeLoader(nil, nil), authorize: &revisionAuthorizerStub{grants: "grant-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			revisions, err := NewPolicyRevisions(test.data, test.authorize)
			require.NoError(t, err)
			failedCtx, revision, err := revisions.ForRequest(priorCtx, principal)
			require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
			require.Empty(t, revision)
			require.ErrorIs(t, a.Authorize(failedCtx, principal, requirement), platformauthz.ErrDecisionUnavailable)
		})
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPolicyRevisionsRequireSnapshotProviderAtConstruction(t *testing.T) {
	t.Parallel()
	_, err := NewPolicyRevisions(nil, nil)
	require.Error(t, err)
	var typedNil *Authorizer
	_, err = NewPolicyRevisions(nil, typedNil)
	require.Error(t, err)
}
