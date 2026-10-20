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
	"github.com/lihongjie0209/go-api-template/internal/resourcecatalog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestCatalogActionTargetResponsesExcludeAuditFields(t *testing.T) {
	t.Parallel()
	resourceID := "6020111e-8d91-47e1-a272-2c81837a2542"
	actionID := "8c5a5b98-0d22-4260-944c-f44b92d941df"
	now := time.Now()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT id,resource_key,name,description,scope,source,status,created_at,created_by,updated_at,updated_by,version FROM authorization_resources`).
		WithArgs(resourceID).WillReturnRows(sqlmock.NewRows([]string{"id", "resource_key", "name", "description", "scope", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow(resourceID, "warehouse.item", "库存物料", "业务资源", "tenant", "manual", "active", now, "admin", now, "admin", 4))
	mock.ExpectQuery(`SELECT a.id,a.resource_id,r.resource_key,r.name AS resource_name`).
		WithArgs(actionID).WillReturnRows(sqlmock.NewRows([]string{"id", "resource_id", "resource_key", "resource_name", "action_key", "name", "description", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow(actionID, resourceID, "warehouse.item", "库存物料", "adjust", "调整库存", "业务动作", "manual", "active", now, "admin", now, "admin", 3))
	handler := NewResourceCatalogHandler(resourcecatalog.NewService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	context := platformprincipal.SystemContext(t.Context(), "admin")
	for _, test := range []struct {
		name     string
		id       string
		call     func(*gin.Context)
		wantKeys int
	}{
		{name: "resource-update", id: resourceID, call: handler.ResourceUpdateTarget, wantKeys: 7},
		{name: "action-delete", id: actionID, call: handler.ActionDeleteTarget, wantKeys: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/authorization/"+test.name, strings.NewReader(`{"id":"`+test.id+`"}`)).WithContext(context)
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.call(ctx)
			require.Equal(t, http.StatusOK, recorder.Code)
			var result struct {
				Body map[string]any `json:"body"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
			require.Len(t, result.Body, test.wantKeys)
			require.Equal(t, test.id, result.Body["id"])
			require.NotContains(t, result.Body, "created_by")
			require.NotContains(t, result.Body, "updated_at")
			require.NotContains(t, result.Body, "source")
		})
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCatalogStaleVersionUsesGlobalCode(t *testing.T) {
	t.Parallel()
	handler := NewResourceCatalogHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/authorization/resources/update", strings.NewReader(`{}`))
	handler.respond(ctx, nil, resourcecatalog.ErrStaleVersion)
	require.Equal(t, http.StatusConflict, recorder.Code)
	var result struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Equal(t, apperror.CodeStaleVersion, result.Code)
}
