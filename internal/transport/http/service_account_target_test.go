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
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestServiceAccountMutationTargetsReturnNarrowIdentity(t *testing.T) {
	for _, route := range []string{"/api/v1/service-accounts/secret/rotate-target", "/api/v1/service-accounts/delete-target"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\? AND deleted_at IS NULL`).
				WithArgs("account-1").
				WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "status", "version"}).AddRow("account-1", "billing-worker", "账单服务", "active", 4))
			handler := NewServiceAccountHandler(serviceaccount.New(sqlx.NewDb(db, "sqlmock"), nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "operator", Type: platformprincipal.TypeUser})
			c.Request = httptest.NewRequest(http.MethodPost, route, strings.NewReader(`{"id":"account-1"}`)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")
			if strings.Contains(route, "rotate-target") {
				handler.RotateTarget(c)
			} else {
				handler.DeleteTarget(c)
			}
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Body ServiceAccountMutationTarget `json:"body"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, ServiceAccountMutationTarget{
				ID: "account-1", ClientID: "billing-worker", Name: "账单服务",
				Status: serviceaccount.StatusActive, Version: 4,
			}, response.Body)
			require.NotContains(t, recorder.Body.String(), "secret")
			require.NotContains(t, recorder.Body.String(), "created_by")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestServiceAccountMutationTargetRejectsMissingID(t *testing.T) {
	t.Parallel()
	handler := NewServiceAccountHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts/secret/rotate-target", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.RotateTarget(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestServiceAccountMutationTargetHidesDeletedAccount(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\? AND deleted_at IS NULL`).
		WithArgs("deleted-account").
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "status", "version"}))
	handler := NewServiceAccountHandler(serviceaccount.New(sqlx.NewDb(db, "sqlmock"), nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "operator", Type: platformprincipal.TypeUser})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts/delete-target", strings.NewReader(`{"id":"deleted-account"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.DeleteTarget(c)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}
