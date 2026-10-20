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
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/application"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestApplicationDeleteTargetReturnsOnlyConfirmationFields(t *testing.T) {
	t.Parallel()
	id := "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT id,code,name,status,version FROM applications WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "status", "version"}).AddRow(id, "erp", "ERP", "active", 2))
	handler := NewApplicationHandler(application.New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/applications/delete-target", strings.NewReader(`{"id":"`+id+`"}`)).WithContext(platformprincipal.SystemContext(t.Context(), "admin"))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.DeleteTarget(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, map[string]any{"id": id, "code": "erp", "name": "ERP", "status": "active", "version": float64(2)}, response.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplicationStaleVersionUsesGlobalErrorCode(t *testing.T) {
	t.Parallel()
	handler := NewApplicationHandler(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/applications/delete", strings.NewReader(`{}`))
	handler.respond(ctx, nil, application.ErrStaleVersion)
	require.Equal(t, http.StatusConflict, recorder.Code)
	var response struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, apperror.CodeStaleVersion, response.Code)
}
