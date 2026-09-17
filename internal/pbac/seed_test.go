package pbac

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSeedIDGoldenMappings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		key      string
		expected string
	}{
		{name: "capability policy", key: "policy:authorization-capability-self-evaluate", expected: "4fbc3f70-cc41-589c-bcdd-4a79896f901b"},
		{name: "capability version", key: "policy-version:authorization-capability-self-evaluate:1", expected: "fce46181-1293-5d4d-adb8-2adb9157b20e"},
		{name: "capability action", key: "policy-action:authorization-capability-self-evaluate:authorization.capability:evaluate", expected: "b49c52be-fec0-5451-aebd-39a42608b396"},
		{name: "profile policy", key: "policy:identity-profile-self-service", expected: "6a7ff4b6-003e-5709-be92-1d939b4d889b"},
		{name: "profile version", key: "policy-version:identity-profile-self-service:1", expected: "c3ea4d30-3e91-57a6-a0be-1aeb78273499"},
		{name: "profile read action", key: "policy-action:identity-profile-self-service:identity.profile:read", expected: "97b74493-f271-520a-8f7c-806e56873fa1"},
		{name: "profile update action", key: "policy-action:identity-profile-self-service:identity.profile:update", expected: "8e519fe1-f901-50b7-96d4-db284646a975"},
		{name: "frontend telemetry policy", key: "policy:frontend-telemetry-self-record", expected: "f1014488-7172-5e97-9073-1bdef7fe18bf"},
		{name: "frontend telemetry version", key: "policy-version:frontend-telemetry-self-record:1", expected: "d28248b4-d7f5-58f0-b4e4-26aa94f0c75c"},
		{name: "frontend telemetry action", key: "policy-action:frontend-telemetry-self-record:frontend.telemetry:record", expected: "b7934482-cd1c-5967-bba3-b2da0c363929"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual, err := SeedID(test.key)
			require.NoError(t, err)
			require.Equal(t, test.expected, actual)
		})
	}
}
