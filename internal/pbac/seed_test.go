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
