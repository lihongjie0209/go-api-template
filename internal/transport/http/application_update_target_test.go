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
	"github.com/lihongjie0209/go-api-template/internal/application"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestApplicationUpdateTargetReturnsOnlyEditableFields(t *testing.T) {
	t.Parallel()
	id := "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT id,code,name,description,icon,home_path,status,sort_order,metadata,version FROM applications WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{
		"id", "code", "name", "description", "icon", "home_path", "status", "sort_order", "metadata", "version",
	}).AddRow(id, "erp", "ERP", "description", "icon", "/erp", "active", int64(2), []byte(`{"x":1}`), int64(4)))
	handler := NewApplicationHandler(application.New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/applications/update-target", strings.NewReader(`{"id":"`+id+`"}`)).WithContext(platformprincipal.SystemContext(t.Context(), "admin"))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateTarget(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, map[string]any{
		"id": id, "code": "erp", "name": "ERP", "description": "description", "icon": "icon",
		"home_path": "/erp", "status": "active", "sort_order": float64(2), "metadata": map[string]any{"x": float64(1)}, "version": float64(4),
	}, response.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}
