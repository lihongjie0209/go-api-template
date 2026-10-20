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
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/navigation"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestNavigationDeleteTargetResponseIsNarrow(t *testing.T) {
	t.Parallel()
	const id = "63b3212e-e383-5f26-ad10-c4e30d778918"
	const appID = "bed5ee41-d555-5f37-b357-6f8f2314f096"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT n.id,n.application_id,a.name AS application_name,n.navigation_key`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{
		"id", "application_id", "application_name", "navigation_key", "name", "navigation_type", "version",
	}).AddRow(id, appID, "仓储应用", "inventory", "库存", "menu", 4))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := navigation.New(sqlx.NewDb(raw, "pgx"), nil, nil, nil, nil, nil, logger, config.Config{})
	handler := NewNavigationHandler(service, nil, logger)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/navigations/delete-target", strings.NewReader(`{"id":"`+id+`"}`)).
		WithContext(platformprincipal.SystemContext(t.Context(), "admin"))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.DeleteTarget(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var result struct {
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Len(t, result.Body, 7)
	require.Equal(t, "仓储应用", result.Body["application_name"])
	require.NotContains(t, result.Body, "created_by")
	require.NotContains(t, result.Body, "metadata")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNavigationStaleVersionUsesGlobalCode(t *testing.T) {
	t.Parallel()
	handler := NewNavigationHandler(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/navigations/update", strings.NewReader(`{}`))
	handler.respond(ctx, nil, navigation.ErrStaleVersion)
	require.Equal(t, http.StatusConflict, recorder.Code)
	var result struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Equal(t, apperror.CodeStaleVersion, result.Code)
}
