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
	"github.com/lihongjie0209/go-api-template/internal/permission"
	"github.com/stretchr/testify/require"
)

func TestPermissionNodeActionTargetHandlersReturnNarrowFields(t *testing.T) {
	const id = "08896c3a-4c82-473e-90c8-b59f60446139"
	for _, tc := range []struct {
		name string
		call func(*PermissionHandler, *gin.Context)
		want map[string]any
	}{
		{"update", (*PermissionHandler).UpdateTarget, map[string]any{
			"id": id, "parent_id": nil, "parent_name": "", "permission_key": "platform.user.read",
			"name": "读取用户", "node_type": "permission", "resource": "identity.user", "action": "read",
			"description": "说明", "sort_order": float64(3), "status": "active", "has_children": false, "version": float64(4),
		}},
		{"delete", (*PermissionHandler).DeleteTarget, map[string]any{
			"id": id, "permission_key": "platform.user.read", "name": "读取用户",
			"node_type": "permission", "parent_name": "", "version": float64(4),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			now := time.Now()
			mock.ExpectQuery(`SELECT p\.id.*FROM permissions p LEFT JOIN permissions parent`).WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{
					"id", "parent_id", "parent_name", "permission_key", "name", "node_type", "resource", "action",
					"description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version",
				}).AddRow(id, nil, "", "platform.user.read", "读取用户", "permission", "identity.user", "read", "说明", 3, "active", false, now, "creator", now, "updater", 4))
			if tc.name == "update" {
				mock.ExpectQuery(`SELECT count\(\*\) FROM permissions WHERE parent_id=\? AND deleted_at IS NULL`).WithArgs(id).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			handler := NewPermissionHandler(permission.New(permission.NewRepository(sqlx.NewDb(raw, "sqlmock")), nil, nil, nil, nil, nil, logger), logger)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/permissions/"+tc.name+"-target", strings.NewReader(`{"id":"`+id+`"}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			tc.call(handler, ctx)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Body map[string]any `json:"body"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, tc.want, response.Body)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPermissionNodeActionTargetHandlersRejectMissingID(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*PermissionHandler, *gin.Context)
	}{
		{"update", (*PermissionHandler).UpdateTarget},
		{"delete", (*PermissionHandler).DeleteTarget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewPermissionHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/permissions/"+tc.name+"-target", strings.NewReader(`{}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			tc.call(handler, ctx)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}
