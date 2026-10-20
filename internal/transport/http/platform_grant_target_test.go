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
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestPlatformGrantActionTargetResponses(t *testing.T) {
	t.Parallel()
	id := "c3719221-837d-4492-a819-592700987773"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT g.id,g.principal_type,g.principal_id,.*g.version FROM platform_principal_permissions g`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "principal_type", "principal_id", "principal_name", "principal_identifier", "permission_id", "permission_key", "permission_name", "version"}).
		AddRow(id, "user", "0d83daa9-f20a-4eae-ad2e-2897287b2120", "张三", "zhangsan", "563a8038-7e68-42cd-9220-997b8c714e5c", "platform.user.read", "查看用户", 7))
	mock.ExpectQuery(`SELECT g.id,g.principal_type,COALESCE.*g.version FROM platform_principal_permissions g`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "principal_type", "principal_name", "principal_identifier", "permission_key", "permission_name", "version"}).
		AddRow(id, "user", "张三", "zhangsan", "platform.user.read", "查看用户", 7))
	handler := NewPlatformGrantHandler(authorization.NewPlatformGrantService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	requestContext := platformprincipal.SystemContext(t.Context(), "admin")
	for _, test := range []struct {
		name     string
		call     func(*gin.Context)
		wantKeys int
	}{
		{name: "update", call: handler.UpdateTarget, wantKeys: 9},
		{name: "delete", call: handler.DeleteTarget, wantKeys: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform/permission-grants/"+test.name+"-target", strings.NewReader(`{"id":"`+id+`"}`)).WithContext(requestContext)
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.call(ctx)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Body map[string]any `json:"body"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Len(t, response.Body, test.wantKeys)
			require.Equal(t, id, response.Body["id"])
			require.NotContains(t, response.Body, "created_by")
			require.NotContains(t, response.Body, "updated_at")
		})
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlatformGrantStaleVersionUsesGlobalCode(t *testing.T) {
	t.Parallel()
	handler := NewPlatformGrantHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform/permission-grants/update", strings.NewReader(`{}`))
	handler.respond(ctx, nil, authorization.ErrPlatformGrantStaleVersion)
	require.Equal(t, http.StatusConflict, recorder.Code)
	var response struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, apperror.CodeStaleVersion, response.Code)
}
