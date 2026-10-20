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
	"github.com/lihongjie0209/go-api-template/internal/scheduler"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestScheduledJobActionTargetsReturnActionOnlyProjections(t *testing.T) {
	t.Parallel()
	id := "8b74cc0b-ea4e-4c3c-8cb2-8f9d83b75fa0"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	columns := []string{"id", "code", "name", "description", "cron_spec", "timezone", "handler", "timeout_seconds", "lock_ttl_seconds", "status", "payload", "created_at", "created_by", "updated_at", "updated_by", "version"}
	now := time.Now()
	for range 2 {
		mock.ExpectQuery(`SELECT .* FROM scheduled_jobs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).
			WillReturnRows(sqlmock.NewRows(columns).AddRow(id, "report", "Report", "desc", "0 * * * * *", "Asia/Shanghai", "report.run", 60, 120, "active", []byte(`{"a":1}`), now, "creator", now, "updater", 4))
	}
	handler := NewScheduledJobHandler(scheduler.NewDefinitionService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, nil), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	for _, test := range []struct {
		path     string
		call     func(*gin.Context)
		wantKeys []string
	}{
		{"/api/v1/scheduled-jobs/update-target", handler.UpdateTarget, []string{"id", "code", "name", "description", "cron_spec", "timezone", "handler", "timeout_seconds", "lock_ttl_seconds", "status", "payload", "version"}},
		{"/api/v1/scheduled-jobs/delete-target", handler.DeleteTarget, []string{"id", "code", "name", "status", "version"}},
	} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{"id":"`+id+`"}`)).WithContext(ctx)
		c.Request.Header.Set("Content-Type", "application/json")
		test.call(c)
		require.Equal(t, http.StatusOK, recorder.Code)
		var response struct {
			Body map[string]any `json:"body"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		require.Len(t, response.Body, len(test.wantKeys))
		for _, key := range test.wantKeys {
			require.Contains(t, response.Body, key)
		}
		require.NotContains(t, response.Body, "created_by")
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestScheduledJobStaleVersionUsesGlobalCode(t *testing.T) {
	t.Parallel()
	handler := NewScheduledJobHandler(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/scheduled-jobs/update", strings.NewReader(`{}`))
	handler.respond(c, nil, scheduler.ErrDefinitionStaleVersion)
	require.Equal(t, http.StatusConflict, recorder.Code)
	var response struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, apperror.CodeStaleVersion, response.Code)
}
