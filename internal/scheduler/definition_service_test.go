package scheduler

import (
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/stretchr/testify/require"
)

func TestValidDefinitionPage(t *testing.T) {
	t.Parallel()
	from, to := time.Now(), time.Now().Add(time.Hour)
	require.True(t, validDefinitionPage(DefinitionPageInput{IDs: []string{"job-1"}, Codes: []string{"reconcile"}, Handlers: []string{"orders.reconcile"}, Statuses: []string{"active"}, CreatedAtFrom: &from, CreatedAtTo: &to}, "orders"))
	require.False(t, validDefinitionPage(DefinitionPageInput{Statuses: []string{"unknown"}}, ""))
	require.False(t, validDefinitionPage(DefinitionPageInput{CreatedAtFrom: &to, CreatedAtTo: &from}, ""))
}

func TestDefinitionOrderUsesAllowlist(t *testing.T) {
	t.Parallel()
	order, err := definitionOrder([]pagination.Sort{{Field: "updated_at", Direction: "desc"}})
	require.NoError(t, err)
	require.Equal(t, "updated_at DESC,id ASC", order)
	_, err = definitionOrder([]pagination.Sort{{Field: "code;drop table scheduled_jobs", Direction: "asc"}})
	require.ErrorIs(t, err, ErrInvalidDefinition)
}

func TestRunOrderUsesAllowlist(t *testing.T) {
	t.Parallel()
	order, err := runOrder([]pagination.Sort{{Field: "duration_ms", Direction: "asc"}})
	require.NoError(t, err)
	require.Equal(t, "duration_ms ASC,id ASC", order)
	_, err = runOrder([]pagination.Sort{{Field: "request_id", Direction: "asc"}})
	require.ErrorIs(t, err, ErrInvalidDefinition)
}
