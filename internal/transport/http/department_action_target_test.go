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
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/tenant"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestDepartmentActionTargetHandlersReturnNarrowProjection(t *testing.T) {
	const id = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"
	for _, test := range []struct {
		name string
		path string
		call func(*DepartmentHandler, *gin.Context)
		want map[string]any
	}{
		{
			name: "update", path: "/api/v1/tenant-departments/update-target",
			call: (*DepartmentHandler).UpdateTarget,
			want: map[string]any{"id": id, "parent_id": nil, "parent_name": "", "code": "engineering", "name": "Engineering", "sort_order": float64(10), "version": float64(7)},
		},
		{
			name: "delete", path: "/api/v1/tenant-departments/delete-target",
			call: (*DepartmentHandler).DeleteTarget,
			want: map[string]any{"id": id, "code": "engineering", "name": "Engineering", "version": float64(7)},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			now := time.Now()
			mock.ExpectQuery(`SELECT .* FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL AND \(1 = 1\)`).
				WithArgs("tenant-a", id).
				WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}).
					AddRow(id, "tenant-a", nil, "engineering", "Engineering", 10, now, "creator", now, "updater", 7))
			handler := NewDepartmentHandler(tenant.NewDepartmentService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, config.Config{}, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			principalContext := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
				ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "member-a",
			})
			context.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{"id":"`+id+`"}`)).WithContext(principalContext)
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

func TestDepartmentStaleVersionUsesGlobalErrorCode(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenant-departments/update", strings.NewReader(`{}`))
	handler := NewDepartmentHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler.fail(context, tenant.ErrStaleVersion)
	require.Equal(t, http.StatusConflict, recorder.Code)
	var response struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, apperror.CodeStaleVersion, response.Code)
}
