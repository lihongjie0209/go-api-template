package datapermission

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

func TestRepositoryGetRestrictsTenantVisibilityInSQL(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	repository := NewRepository(sqlx.NewDb(raw, "sqlmock"))
	now := time.Now()

	mock.ExpectQuery(`SELECT id,code,name,scope,tenant_id.*tenant_id=\?`).
		WithArgs("policy-1", "tenant-1").
		WillReturnRows(policyRows().AddRow("policy-1", "member-scope", "Member scope", "tenant", "tenant-1", 1, "active", now, "actor-1", now, "actor-1", 2))

	record, err := repository.Get(t.Context(), "policy-1", "tenant-1")
	require.NoError(t, err)
	require.Equal(t, "tenant-1", record.TenantID.String)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryGetPlatformContextCanOnlySeeGlobalPolicy(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	repository := NewRepository(sqlx.NewDb(raw, "sqlmock"))

	mock.ExpectQuery(`SELECT id,code,name,scope,tenant_id.*scope='global'`).
		WithArgs("tenant-policy").
		WillReturnRows(policyRows())

	_, err = repository.Get(t.Context(), "tenant-policy", "")
	require.ErrorIs(t, err, ErrPolicyNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryGetVersionAppliesParentPolicyVisibility(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	repository := NewRepository(sqlx.NewDb(raw, "sqlmock"))
	now := time.Now()

	mock.ExpectQuery(`JOIN data_permission_policies p.*p.tenant_id=\?`).
		WithArgs("policy-1", int64(3), "tenant-1").
		WillReturnRows(versionRows().AddRow("version-3", "policy-1", 3, "document", "draft", nil, nil, now, "actor-1", now, "actor-1", 1))

	record, err := repository.GetVersion(t.Context(), "policy-1", 3, "tenant-1")
	require.NoError(t, err)
	require.Equal(t, int64(3), record.VersionNumber)
	require.NoError(t, mock.ExpectationsWereMet())
}

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
