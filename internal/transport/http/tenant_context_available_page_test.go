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
	"github.com/lihongjie0209/go-api-template/internal/tenant"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestAvailableTenantPageReturnsScopedEnvelope(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	joined := time.Date(2026, time.October, 1, 2, 3, 4, 0, time.UTC)
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants t`).
		WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT m.tenant_id,t.code tenant_code`).
		WithArgs("user-1", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "tenant_code", "tenant_name", "membership_id", "is_administrator", "joined_at"}).
			AddRow("tenant-1", "tenant", "Tenant", "member-1", true, joined))
	contexts := tenant.NewContextService(sqlx.NewDb(raw, "sqlmock"), nil, config.Config{}, nil)
	handler := NewTenantMemberHandler(nil, contexts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenant-context/available/page", strings.NewReader(`{"page":1,"page_size":20}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.AvailableTenantsPage(c)
	require.Equal(t, http.StatusOK, writer.Code)
	var response struct {
		Code int `json:"code"`
		Body struct {
			Items []tenant.AvailableTenant `json:"items"`
			Page  int                      `json:"page"`
			Total int64                    `json:"total"`
		} `json:"body"`
	}
	require.NoError(t, json.Unmarshal(writer.Body.Bytes(), &response))
	require.Zero(t, response.Code)
	require.Equal(t, 1, response.Body.Page)
	require.Equal(t, int64(1), response.Body.Total)
	require.Len(t, response.Body.Items, 1)
	require.Equal(t, "tenant-1", response.Body.Items[0].TenantID)
	require.Equal(t, "2026-10-01T10:03:04+08:00", response.Body.Items[0].JoinedAt.Format(time.RFC3339))
	require.NoError(t, mock.ExpectationsWereMet())
}
