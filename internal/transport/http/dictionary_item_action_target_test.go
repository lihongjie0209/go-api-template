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

func TestDictionaryItemActionTargetHandlersReturnNarrowFields(t *testing.T) {
	const id = "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	const dictionaryID = "63b3212e-e383-5f26-ad10-c4e30d778918"
	for _, tc := range []struct {
		name string
		call func(*DictionaryHandler, *gin.Context)
		want map[string]any
	}{
		{"update", (*DictionaryHandler).ItemUpdateTarget, map[string]any{
			"id": id, "dictionary_id": dictionaryID, "parent_id": nil, "parent_name": "",
			"code": "gd", "name": "广东", "value": "guangdong", "disabled": false,
			"sort_order": float64(2), "extension": map[string]any{"region": "south"}, "version": float64(4),
		}},
		{"delete", (*DictionaryHandler).ItemDeleteTarget, map[string]any{
			"id": id, "dictionary_id": dictionaryID, "code": "gd", "name": "广东", "version": float64(4),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			now := time.Now()
			mock.ExpectQuery(`SELECT .* FROM dictionary_items WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{
					"id", "dictionary_id", "parent_id", "code", "name", "value", "disabled", "sort_order", "extension",
					"created_at", "created_by", "updated_at", "updated_by", "version",
				}).AddRow(id, dictionaryID, nil, "gd", "广东", "guangdong", false, 2, []byte(`{"region":"south"}`), now, "creator", now, "updater", 4))
			handler := NewDictionaryHandler(dictionary.New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/dictionary-items/"+tc.name+"-target", strings.NewReader(`{"id":"`+id+`"}`)).
				WithContext(platformprincipal.SystemContext(t.Context(), "admin"))
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

func TestDictionaryItemActionTargetHandlersRejectMissingID(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*DictionaryHandler, *gin.Context)
	}{
		{"update", (*DictionaryHandler).ItemUpdateTarget},
		{"delete", (*DictionaryHandler).ItemDeleteTarget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewDictionaryHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/dictionary-items/"+tc.name+"-target", strings.NewReader(`{}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			tc.call(handler, ctx)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}
