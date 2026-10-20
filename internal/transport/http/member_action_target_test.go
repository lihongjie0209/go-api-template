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

func TestMemberActionTargetsReturnNarrowTenantScopedConfirmation(t *testing.T) {
	const memberID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"
	for _, mode := range []struct {
		name string
		path string
		run  func(*TenantMemberHandler, *gin.Context)
	}{
		{name: "status", path: "/api/v1/tenant-members/status/target", run: (*TenantMemberHandler).StatusTarget},
		{name: "remove", path: "/api/v1/tenant-members/remove-target", run: (*TenantMemberHandler).RemoveTarget},
	} {
		t.Run(mode.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			now := time.Now()
			mock.ExpectQuery(`SELECT .* FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\?`).
				WithArgs("tenant-1", memberID).
				WillReturnRows(sqlmock.NewRows([]string{
					"id", "tenant_id", "user_id", "username", "display_name", "status", "joined_at",
					"created_at", "created_by", "updated_at", "updated_by", "version",
				}).AddRow(memberID, "tenant-1", "user-1", "alice", "Alice", "active", now, now, "creator", now, "updater", 7))
			service := tenant.NewMembershipService(
				tenant.NewRepository(sqlx.NewDb(raw, "sqlmock")), nil, nil, nil, nil,
				config.Config{}, nil, nil, nil,
			)
			handler := NewTenantMemberHandler(service, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
				ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1",
			})
			c.Request = httptest.NewRequest(http.MethodPost, mode.path, strings.NewReader(`{"id":"`+memberID+`"}`)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")
			mode.run(handler, c)
			require.Equal(t, http.StatusOK, writer.Code)
			var response struct {
				Code int            `json:"code"`
				Body map[string]any `json:"body"`
			}
			require.NoError(t, json.Unmarshal(writer.Body.Bytes(), &response))
			require.Zero(t, response.Code)
			require.Equal(t, map[string]any{
				"id": memberID, "username": "alice", "display_name": "Alice", "status": "active", "version": float64(7), "is_self": false,
			}, response.Body)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestMemberActionTargetRejectsInvalidID(t *testing.T) {
	handler := NewTenantMemberHandler(
		tenant.NewMembershipService(nil, nil, nil, nil, nil, config.Config{}, nil, nil, nil),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1",
	})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenant-members/status/target", strings.NewReader(`{"id":"bad"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.StatusTarget(c)
	require.Equal(t, http.StatusBadRequest, writer.Code)
}
