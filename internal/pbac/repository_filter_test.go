package pbac

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPolicyPageFiltersAreBoundedAndUseHalfOpenRanges(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	require.True(t, validPolicyPageFilters([]string{"policy-1"}, []string{"member-owner"}, &from, &to, nil, nil))
	require.False(t, validPolicyPageFilters([]string{" policy-1"}, nil, nil, nil, nil, nil))
	require.False(t, validPolicyPageFilters(nil, nil, &to, &from, nil, nil))

	where, args := appendStringSetFilter("scope='global'", nil, "id", []string{"policy-1", "policy-2"})
	where, args = appendTimeRange(where, args, "created_at", &from, &to)
	require.Equal(t, "scope='global' AND id IN (?,?) AND created_at>=? AND created_at<?", where)
	require.Equal(t, []any{"policy-1", "policy-2", from, to}, args)
}
