package application

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateAndStableID(t *testing.T) {
	input := Normalize(Input{Code: " Console ", Name: "Console", Status: "active"})
	require.NoError(t, Validate(input))
	require.Equal(t, "console", input.Code)
	first, err := StableID(input.Code)
	require.NoError(t, err)
	second, err := StableID("console")
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.NotEmpty(t, first)
}

func TestValidateRejectsInvalidApplication(t *testing.T) {
	tests := []struct {
		name  string
		input Input
	}{
		{name: "invalid code", input: Input{Code: "Console!", Name: "Console", Status: "active"}},
		{name: "empty name", input: Input{Code: "console", Status: "active"}},
		{name: "invalid path", input: Input{Code: "console", Name: "Console", HomePath: "/../admin", Status: "active"}},
		{name: "invalid metadata", input: Input{Code: "console", Name: "Console", Status: "active", Metadata: []byte(`[]`)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { require.ErrorIs(t, Validate(test.input), ErrInvalid) })
	}
}
