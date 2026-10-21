package application

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplicationPageJSONContract(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "applications", value: Page{Items: []Record{}, Page: 1, PageSize: 20}},
		{name: "tenant grants", value: GrantPage{Items: []Grant{}, Page: 1, PageSize: 20}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(test.value)
			require.NoError(t, err)
			require.JSONEq(t, `{"items":[],"page":1,"page_size":20,"total":0}`, string(encoded))
		})
	}
}
