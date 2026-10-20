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
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/dictionary"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestDictionaryItemDefinitionTargetHandlers(t *testing.T) {
	const id = "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	for _, tc := range []struct {
		name string
		call func(*DictionaryHandler, *gin.Context)
	}{
		{"read", (*DictionaryHandler).ItemDefinitionTargetRead},
		{"list", (*DictionaryHandler).ItemDefinitionTargetList},
		{"create", (*DictionaryHandler).ItemDefinitionTargetCreate},
		{"update", (*DictionaryHandler).ItemDefinitionTargetUpdate},
		{"delete", (*DictionaryHandler).ItemDefinitionTargetDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			now := time.Now()
			mock.ExpectQuery(`SELECT .* FROM dictionary_definitions WHERE id=\? AND deleted_at IS NULL`).
				WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{"id", "dictionary_code", "name", "dictionary_type", "source_type", "description", "status", "extension", "created_at", "created_by", "updated_at", "updated_by", "version"}).
					AddRow(id, "status", "状态", "enum", "static", "private description", "active", []byte(`{}`), now, "creator", now, "updater", 4))
			handler := NewDictionaryHandler(dictionary.New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			principalCtx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "operator", Type: platformprincipal.TypeUser})
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/dictionary-items/definition-target/"+tc.name, strings.NewReader(`{"id":"`+id+`"}`)).WithContext(principalCtx)
			ctx.Request.Header.Set("Content-Type", "application/json")
			tc.call(handler, ctx)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Code int            `json:"code"`
				Body map[string]any `json:"body"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Zero(t, response.Code)
			require.Equal(t, map[string]any{"id": id, "code": "status", "name": "状态", "type": "enum", "source": "static", "status": "active"}, response.Body)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDictionaryItemDefinitionTargetRejectsMissingID(t *testing.T) {
	handler := NewDictionaryHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/dictionary-items/definition-target/read", strings.NewReader(`{}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.ItemDefinitionTargetRead(ctx)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
