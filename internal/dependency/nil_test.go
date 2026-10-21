package dependency

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsNil(t *testing.T) {
	t.Parallel()
	var pointer *int
	var values []int
	for _, test := range []struct {
		name  string
		value any
		want  bool
	}{
		{name: "nil interface", want: true},
		{name: "typed nil pointer", value: pointer, want: true},
		{name: "typed nil slice", value: values, want: true},
		{name: "value", value: 42},
		{name: "non-nil pointer", value: new(int)},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, IsNil(test.value))
		})
	}
}
