package httptransport

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/config"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestRoleGrantTargetReturnsNarrowRoleAndLinkedPermissions(t *testing.T) {
	t.Parallel()
	const roleID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	now := time.Now()
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status,tr.created_at,tr.created_by,tr.updated_at,tr.updated_by,tr.version FROM tenant_roles tr`).
		WithArgs("tenant-1", roleID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow(roleID, "tenant-1", "reviewer", "Reviewer", "private", "active", now, "system", now, "system", 7))
	mock.ExpectQuery(`SELECT p.id,p.permission_key,p.name,p.resource,p.action,p.status FROM tenant_role_permissions rp`).
		WithArgs("tenant-1", roleID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name", "resource", "action", "status"}).
			AddRow("perm-1", "tenant.member.read", "Read members", "tenant.member", "read", "disabled"))
	service := authorization.NewTenantAuthorizationService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, config.Config{}, nil)
	handler := NewTenantAuthorizationHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1",
	})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenant-roles/permissions/target", strings.NewReader(`{"id":"`+roleID+`"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.RoleGrantTarget(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Code int            `json:"code"`
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Zero(t, response.Code)
	require.Equal(t, map[string]any{
		"id": roleID, "code": "reviewer", "name": "Reviewer", "status": "active", "version": float64(7),
		"permissions": []any{map[string]any{
			"id": "perm-1", "permission_key": "tenant.member.read", "name": "Read members",
			"resource": "tenant.member", "action": "read", "status": "disabled", "assignable": false,
		}},
	}, response.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRoleGrantTargetRejectsMissingID(t *testing.T) {
	t.Parallel()
	handler := NewTenantAuthorizationHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenant-roles/permissions/target", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.RoleGrantTarget(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
