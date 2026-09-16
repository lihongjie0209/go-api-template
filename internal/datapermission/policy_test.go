package datapermission

import (
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/stretchr/testify/require"
)

func TestDataPolicyUsesSharedBoundedSubjectContract(t *testing.T) {
	t.Parallel()
	schema, err := NewSchemaRegistry(memberSchema(t))
	require.NoError(t, err)
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)

	policy := dataPolicyFixture()
	policy.Spec.Subject.Roles.AnyOf = []string{"manager", "manager"}
	_, err = policy.Compile(schema, resources)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestDataPolicyNormalizesSingleRoleAuthoringField(t *testing.T) {
	t.Parallel()
	policy := dataPolicyFixture()
	policy.Spec.Subject.Role = "manager"
	policy.normalize()
	require.Empty(t, policy.Spec.Subject.Role)
	require.Contains(t, policy.Spec.Subject.Roles.AnyOf, "manager")
}
