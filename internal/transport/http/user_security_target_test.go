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
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestUserSecurityTargetsReturnNarrowConfirmation(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"reset-password", "force-logout"} {
		t.Run(route, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			id := "d5298761-7e87-4393-b660-8d0c939a570b"
			mock.ExpectQuery(`SELECT id,username,display_name,status FROM identity_users WHERE id=\? AND status<>'closed' AND deleted_at IS NULL`).
				WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status"}).AddRow(id, "alice", "Alice", "active"))
			handler := NewUserHandler(identity.New(identity.NewRepository(sqlx.NewDb(raw, "sqlmock")), nil, nil, nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			ctx := platformprincipal.SystemContext(t.Context(), "tester")
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/users/security-target/"+route, strings.NewReader(`{"id":"`+id+`"}`)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")
			if route == "reset-password" {
				handler.ResetPasswordTarget(c)
			} else {
				handler.ForceLogoutTarget(c)
			}
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Body map[string]any `json:"body"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, map[string]any{"id": id, "username": "alice", "display_name": "Alice", "status": "active"}, response.Body)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUserDeleteTargetReturnsVersionWithoutContactData(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	id := "d5298761-7e87-4393-b660-8d0c939a570b"
	mock.ExpectQuery(`SELECT id,username,display_name,status,version FROM identity_users WHERE id=\? AND status<>'closed' AND deleted_at IS NULL`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status", "version"}).AddRow(id, "alice", "Alice", "active", 3))
	handler := NewUserHandler(identity.New(identity.NewRepository(sqlx.NewDb(raw, "sqlmock")), nil, nil, nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/users/delete-target", strings.NewReader(`{"id":"`+id+`"}`)).WithContext(platformprincipal.SystemContext(t.Context(), "tester"))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.DeleteTarget(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, map[string]any{"id": id, "username": "alice", "display_name": "Alice", "status": "active", "version": float64(3)}, response.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}
