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

func TestPermissionActionEditTargetReturnsNarrowVersionedResponse(t *testing.T) {
	const permissionID = "08896c3a-4c82-473e-90c8-b59f60446139"
	const actionID = "18f4762f-f4f9-47d5-bd7a-6f07af436f58"
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	mock.ExpectQuery(`SELECT p\.id.*FROM permissions p LEFT JOIN permissions parent`).
		WithArgs(permissionID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "parent_id", "parent_name", "permission_key", "name", "node_type", "resource", "action", "description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow(permissionID, nil, "", "platform.user.read", "Read users", "permission", "identity.user", "read", "private description", 0, "active", false, now, "creator", now, "updater", 4))
	mock.ExpectQuery(`SELECT a\.id,r\.resource_key,a\.action_key`).
		WithArgs(permissionID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "resource_key", "action_key", "resource_name", "action_name", "scope"}).
			AddRow(actionID, "identity.user", "read", "Users", "Read", "platform"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := permission.New(permission.NewRepository(sqlx.NewDb(db, "sqlmock")), nil, nil, nil, nil, nil, logger)
	handler := NewPermissionHandler(service, logger)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/permissions/actions/target", strings.NewReader(`{"id":"`+permissionID+`"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.ActionEditTarget(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Code int            `json:"code"`
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Zero(t, response.Code)
	require.Equal(t, map[string]any{
		"id":             permissionID,
		"permission_key": "platform.user.read",
		"name":           "Read users",
		"resource":       "identity.user",
		"action":         "read",
		"version":        float64(4),
		"actions": []any{map[string]any{
			"id": actionID, "resource_key": "identity.user", "action_key": "read",
			"resource_name": "Users", "action_name": "Read", "scope": "platform",
		}},
	}, response.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPermissionActionEditTargetRejectsMissingID(t *testing.T) {
	handler := NewPermissionHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/permissions/actions/target", strings.NewReader(`{}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.ActionEditTarget(ctx)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
