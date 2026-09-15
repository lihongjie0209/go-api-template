package tenant

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateDepartmentMove(t *testing.T) {
	root, child := "root", "child"
	tests := []struct {
		name, id, parent string
		parents          map[string]*string
		wantErr          bool
	}{
		{name: "move to sibling", id: "child", parent: "sibling", parents: map[string]*string{"sibling": &root}, wantErr: false},
		{name: "move under descendant", id: "root", parent: "grandchild", parents: map[string]*string{"grandchild": &child, "child": &root}, wantErr: true},
		{name: "corrupt existing cycle fails closed", id: "other", parent: "a", parents: map[string]*string{"a": ptr("b"), "b": ptr("a")}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDepartmentMove(test.id, test.parent, test.parents)
			if test.wantErr {
				require.ErrorIs(t, err, ErrInvalid)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestFilterDepartmentsRetainsAncestorsAndSearchesCode(t *testing.T) {
	root, group := "root", "group"
	records := []Department{
		{ID: root, Code: "company", Name: "公司"},
		{ID: group, ParentID: &root, Code: "engineering", Name: "研发"},
		{ID: "backend", ParentID: &group, Code: "backend_platform", Name: "后端"},
		{ID: "sales", ParentID: &root, Code: "sales", Name: "销售"},
	}
	require.Equal(t, records[:3], filterDepartments(records, "BACKEND"))
	missing := filterDepartments(records, "missing")
	require.NotNil(t, missing)
	require.Empty(t, missing)
}
func ptr(value string) *string { return &value }
