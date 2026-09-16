package tenant

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestMembershipServiceMissingDataScopeFailsClosed(t *testing.T) {
	t.Parallel()
	service := &MembershipService{}
	userContext := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1",
	})
	_, err := service.memberScope(userContext)
	require.True(t, errors.Is(err, datapermission.ErrScopeRequired))

	systemContext := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "scheduler", Type: platformprincipal.TypeSystem, TenantID: "tenant-1",
	})
	scope, err := service.memberScope(systemContext)
	require.NoError(t, err)
	require.Equal(t, "(1 = 1)", scope.Clause)
}

func TestPageMembersAppliesIdenticalDataScopeToCountAndItems(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	repository := NewRepository(sqlx.NewDb(raw, "sqlmock"))
	scope := datapermission.SQLPredicate{Clause: "(tm.user_id = ?)", Args: []any{"user-1"}}
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.deleted_at IS NULL AND \(tm.user_id = \?\)`).WithArgs("tenant-1", "user-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT .* FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.deleted_at IS NULL AND \(tm.user_id = \?\).*LIMIT \? OFFSET \?`).WithArgs("tenant-1", "user-1", 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "user_id", "username", "display_name", "status", "joined_at", "created_at", "created_by", "updated_at", "updated_by", "version"}))

	items, total, err := repository.PageMembers(t.Context(), "tenant-1", MemberPageInput{Request: pagination.Request{Page: 1, PageSize: 20}}, scope)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Zero(t, total)
	require.NoError(t, mock.ExpectationsWereMet())
}
