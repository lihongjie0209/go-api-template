package httptransport

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/config"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestMemberRoleAssignmentTargetReturnsNarrowSnapshot(t *testing.T) {
	t.Parallel()
	const memberID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT tm.id,tm.username,tm.display_name,tm.status,tm.version FROM tenant_memberships tm`).
		WithArgs("tenant-1", memberID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status", "version"}).
			AddRow(memberID, "alice", "Alice", "active", 7))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships tm`).
		WithArgs("tenant-1", memberID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT r.id,r.code,r.name,r.status FROM tenant_member_roles mr`).
		WithArgs("tenant-1", memberID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "status"}).
			AddRow("role-1", "auditor", "Auditor", "disabled"))
	service := authorization.NewTenantAuthorizationService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, config.Config{}, nil)
	handler := NewTenantAuthorizationHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "actor-1",
	})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenant-members/roles/target", strings.NewReader(`{"membership_id":"`+memberID+`"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.MemberRoleAssignmentTarget(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Code int            `json:"code"`
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Zero(t, response.Code)
	require.Equal(t, map[string]any{
		"id": memberID, "username": "alice", "display_name": "Alice", "status": "active", "version": float64(7),
		"roles": []any{map[string]any{"id": "role-1", "code": "auditor", "name": "Auditor", "status": "disabled", "assignable": false}},
	}, response.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemberRoleAssignmentTargetRejectsMissingID(t *testing.T) {
	t.Parallel()
	handler := NewTenantAuthorizationHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenant-members/roles/target", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.MemberRoleAssignmentTarget(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
