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

func TestUserUpdateTargetReturnsOnlyEditableFields(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	id := "d5298761-7e87-4393-b660-8d0c939a570b"
	mock.ExpectQuery(`SELECT id,username,display_name,email,phone,status,version FROM identity_users WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "email", "phone", "status", "version"}).AddRow(id, "alice", "Alice", "alice@example.com", "13800000000", "active", 4))
	handler := NewUserHandler(identity.New(identity.NewRepository(sqlx.NewDb(raw, "sqlmock")), nil, nil, nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/users/update-target", strings.NewReader(`{"id":"`+id+`"}`)).WithContext(platformprincipal.SystemContext(t.Context(), "tester"))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateTarget(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, map[string]any{"id": id, "username": "alice", "display_name": "Alice", "email": "alice@example.com", "phone": "13800000000", "status": "active", "version": float64(4)}, response.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserUpdateTargetRejectsMissingID(t *testing.T) {
	t.Parallel()
	handler := NewUserHandler(identity.New(nil, nil, nil, nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/users/update-target", strings.NewReader(`{}`)).WithContext(platformprincipal.SystemContext(t.Context(), "tester"))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateTarget(ctx)
	require.NotEqual(t, http.StatusOK, recorder.Code)
}
