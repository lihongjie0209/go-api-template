package httptransport

import (
	"encoding/json"
	"errors"
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
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestServiceAccountUpdateTargetReturnsOnlyEditableFields(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	expires := time.Date(2027, 2, 3, 4, 5, 0, 0, time.FixedZone("CST", 8*3600))
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\? AND deleted_at IS NULL`).
		WithArgs("account-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_by", "version",
		}).AddRow("account-1", "billing-worker", "账单服务", "账单同步", "active", expires, expires, 3, expires, "admin", 4))
	handler := NewServiceAccountHandler(serviceaccount.New(sqlx.NewDb(db, "sqlmock"), nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "operator", Type: platformprincipal.TypeUser})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts/update-target", strings.NewReader(`{"id":"account-1"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateTarget(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, map[string]any{
		"id": "account-1", "client_id": "billing-worker", "name": "账单服务", "description": "账单同步",
		"status": "active", "expires_at": "2027-02-03T04:05:00+08:00", "version": float64(4),
	}, response.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestServiceAccountUpdateTargetRejectsMissingIDAndDeletedAccount(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewServiceAccountHandler(nil, logger)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts/update-target", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateTarget(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\? AND deleted_at IS NULL`).
		WithArgs("deleted-account").
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "status", "version"}))
	handler = NewServiceAccountHandler(serviceaccount.New(sqlx.NewDb(db, "sqlmock"), nil, nil, nil, config.Config{}), logger)
	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "operator", Type: platformprincipal.TypeUser})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts/update-target", strings.NewReader(`{"id":"deleted-account"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateTarget(c)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestServiceAccountUpdateTargetPropagatesDatabaseFailureWithoutAccountData(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\? AND deleted_at IS NULL`).
		WithArgs("account-1").WillReturnError(errors.New("database unavailable"))
	handler := NewServiceAccountHandler(serviceaccount.New(sqlx.NewDb(db, "sqlmock"), nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "operator", Type: platformprincipal.TypeUser})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts/update-target", strings.NewReader(`{"id":"account-1"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateTarget(c)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "database unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}
