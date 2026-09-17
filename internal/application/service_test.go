package application

import (
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/stretchr/testify/require"
)

func TestValidPageInput(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	require.True(t, validPageInput(PageInput{
		Request:       pagination.Request{Keyword: "console"},
		IDs:           []string{"app-1"},
		Codes:         []string{"console"},
		Statuses:      []string{"active"},
		CreatedAtFrom: &from,
		CreatedAtTo:   &to,
	}, "console"))
	require.False(t, validPageInput(PageInput{Statuses: []string{"unknown"}}, ""))
	require.False(t, validPageInput(PageInput{Codes: []string{"Invalid Code"}}, ""))
	require.False(t, validPageInput(PageInput{CreatedAtFrom: &to, CreatedAtTo: &from}, ""))
}

func TestApplicationOrderUsesAllowlistAndStableTieBreaker(t *testing.T) {
	t.Parallel()
	order, err := applicationOrder([]pagination.Sort{{Field: "created_at", Direction: "desc"}})
	require.NoError(t, err)
	require.Equal(t, "created_at DESC,id ASC", order)
	_, err = applicationOrder([]pagination.Sort{{Field: "created_at; DROP TABLE applications", Direction: "asc"}})
	require.ErrorIs(t, err, ErrInvalid)
}
