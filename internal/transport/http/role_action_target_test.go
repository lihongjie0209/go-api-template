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
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/config"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestRoleActionTargetHandlersReturnOnlyMatchingProjection(t *testing.T) {
	const roleID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"
	for _, test := range []struct {
		name string
		path string
		call func(*TenantAuthorizationHandler, *gin.Context)
		want map[string]any
	}{
		{
			name: "update", path: "/api/v1/tenant-roles/update-target",
			call: (*TenantAuthorizationHandler).RoleUpdateTarget,
			want: map[string]any{"id": roleID, "code": "reviewer", "name": "Reviewer", "description": "Internal", "status": "active", "version": float64(7)},
		},
		{
			name: "delete", path: "/api/v1/tenant-roles/delete-target",
			call: (*TenantAuthorizationHandler).RoleDeleteTarget,
			want: map[string]any{"id": roleID, "code": "reviewer", "name": "Reviewer", "status": "active", "version": float64(7)},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			now := time.Now()
			mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status,tr.created_at,tr.created_by,tr.updated_at,tr.updated_by,tr.version FROM tenant_roles tr`).
				WithArgs("tenant-1", roleID).
				WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
					AddRow(roleID, "tenant-1", "reviewer", "Reviewer", "Internal", "active", now, "system", now, "system", 7))
			service := authorization.NewTenantAuthorizationService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, config.Config{}, nil)
			handler := NewTenantAuthorizationHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			principalContext := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
				ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1",
			})
			context.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{"id":"`+roleID+`"}`)).WithContext(principalContext)
			context.Request.Header.Set("Content-Type", "application/json")
			test.call(handler, context)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Code int            `json:"code"`
				Body map[string]any `json:"body"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Zero(t, response.Code)
			require.Equal(t, test.want, response.Body)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestTenantAuthorizationStaleVersionUsesGlobalErrorCode(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenant-roles/update", strings.NewReader(`{}`))
	handler := NewTenantAuthorizationHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler.fail(context, authorization.ErrTenantAuthorizationStaleVersion)
	require.Equal(t, http.StatusConflict, recorder.Code)
	var response struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, apperror.CodeStaleVersion, response.Code)
}
