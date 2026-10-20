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
	"github.com/lihongjie0209/go-api-template/internal/platformconfig"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestPlatformConfigActionTargetsExposeOnlyActionFields(t *testing.T) {
	t.Parallel()
	id := "20f40b91-76f1-5d16-bade-fc464610cbde"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT id,config_key,name,category,value_type,value,description,is_public,status,version FROM platform_configs WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"id", "config_key", "name", "category", "value_type", "value", "description", "is_public", "status", "version"}).
			AddRow(id, "ui.theme", "Theme", "ui", "object", []byte(`{"color":"blue"}`), "", true, "active", 4))
	mock.ExpectQuery(`SELECT id,config_key,name,is_public,status,version FROM platform_configs WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"id", "config_key", "name", "is_public", "status", "version"}).
			AddRow(id, "ui.theme", "Theme", true, "active", 4))
	handler := NewPlatformConfigHandler(platformconfig.New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	for _, test := range []struct {
		name         string
		call         func(*gin.Context)
		wantKeys     int
		valueVisible bool
	}{
		{name: "update", call: handler.UpdateTarget, wantKeys: 10, valueVisible: true},
		{name: "delete", call: handler.DeleteTarget, wantKeys: 6, valueVisible: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform-configs/"+test.name+"-target", strings.NewReader(`{"id":"`+id+`"}`)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")
			test.call(c)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Body map[string]any `json:"body"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Len(t, response.Body, test.wantKeys)
			require.Equal(t, id, response.Body["id"])
			require.Equal(t, test.valueVisible, response.Body["value"] != nil)
			require.NotContains(t, response.Body, "created_by")
		})
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlatformConfigStaleVersionUsesGlobalCode(t *testing.T) {
	t.Parallel()
	handler := NewPlatformConfigHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform-configs/update", strings.NewReader(`{}`))
	handler.fail(c, platformconfig.ErrStaleVersion)
	require.Equal(t, http.StatusConflict, recorder.Code)
	var response struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, apperror.CodeStaleVersion, response.Code)
}
