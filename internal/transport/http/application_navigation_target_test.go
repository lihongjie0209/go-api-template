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

func TestNavigationApplicationTargetHandlers(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(*ApplicationHandler, *gin.Context)
	}{
		{name: "read", call: (*ApplicationHandler).NavigationApplicationTargetRead},
		{name: "list", call: (*ApplicationHandler).NavigationApplicationTargetList},
		{name: "create", call: (*ApplicationHandler).NavigationApplicationTargetCreate},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			mock.ExpectQuery(`SELECT id,name FROM applications WHERE id=\? AND deleted_at IS NULL`).
				WithArgs("app-1").
				WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow("app-1", "业务应用"))
			handler := NewApplicationHandler(application.New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			principalCtx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "platform-user", Type: platformprincipal.TypeUser})
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/navigations/application-target/"+test.name, strings.NewReader(`{"id":"app-1"}`)).WithContext(principalCtx)
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.call(handler, ctx)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Code int                          `json:"code"`
				Body application.NavigationTarget `json:"body"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Zero(t, response.Code)
			require.Equal(t, application.NavigationTarget{ID: "app-1", Name: "业务应用"}, response.Body)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestNavigationApplicationTargetRejectsMissingID(t *testing.T) {
	t.Parallel()
	handler := NewApplicationHandler(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/navigations/application-target/list", strings.NewReader(`{}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.NavigationApplicationTargetList(ctx)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
