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
	"github.com/lihongjie0209/go-api-template/internal/authentication"
	"github.com/lihongjie0209/go-api-template/internal/config"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestSessionRevokeTargetReturnsOnlyConfirmationFields(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	expires := time.Now().Add(time.Hour)
	mock.ExpectQuery(`SELECT id,client_ip,user_agent,expires_at,revoked_at,version FROM identity_sessions WHERE id=\? AND user_id=\? AND deleted_at IS NULL`).
		WithArgs("session-1", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_ip", "user_agent", "expires_at", "revoked_at", "version"}).AddRow("session-1", "203.0.113.1", "Browser", expires, nil, 4))
	handler := NewUserAuthenticationHandler(authentication.New(sqlx.NewDb(db, "sqlmock"), nil, nil, nil, config.Config{}), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/sessions/revoke-target", strings.NewReader(`{"session_id":"session-1"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.SessionRevokeTarget(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "session-1", response.Body["id"])
	require.Equal(t, "active", response.Body["status"])
	require.Equal(t, float64(4), response.Body["version"])
	require.NotContains(t, recorder.Body.String(), "user_id")
	require.NotContains(t, recorder.Body.String(), "refresh_token")
	require.NotContains(t, recorder.Body.String(), "revoked_at")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionRevokeTargetRejectsMissingID(t *testing.T) {
	t.Parallel()
	handler := NewUserAuthenticationHandler(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/sessions/revoke-target", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.SessionRevokeTarget(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestSessionRevokeVersionConflictUsesGlobalCode(t *testing.T) {
	t.Parallel()
	handler := NewUserAuthenticationHandler(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/sessions/revoke", strings.NewReader(`{}`))
	handler.fail(c, authentication.ErrSessionVersionConflict)
	require.Equal(t, http.StatusConflict, recorder.Code)
	var response struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, apperror.CodeStaleVersion, response.Code)
}
