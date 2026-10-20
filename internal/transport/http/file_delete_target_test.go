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
	"github.com/lihongjie0209/go-api-template/internal/files"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestFileDeleteTargetReturnsOnlyConfirmationFields(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM files WHERE id=\? AND tenant_id=\? AND deleted_at IS NULL`).
		WithArgs("file-a", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "object_key", "original_name", "content_type", "size_bytes", "etag", "checksum_sha256", "created_at", "created_by", "updated_at", "updated_by", "version", "deleted_at", "deleted_by", "object_deleted_at", "object_delete_attempts", "object_delete_error", "object_delete_next_at"}).
			AddRow("file-a", "tenant-a", "private/object-key", "报告.pdf", "application/pdf", 12, "etag", "hash", now, "creator", now, "updater", 4, nil, nil, nil, 0, "", nil))
	handler := NewFileHandler(files.New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, nil, config.Config{Files: config.Files{Enabled: true}}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "operator", Type: platformprincipal.TypeUser, TenantID: "tenant-a"})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/files/delete-target", strings.NewReader(`{"id":"file-a"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.DeleteTarget(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Code int              `json:"code"`
		Body FileDeleteTarget `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Zero(t, response.Code)
	require.Equal(t, FileDeleteTarget{ID: "file-a", OriginalName: "报告.pdf", Version: 4}, response.Body)
	require.NotContains(t, recorder.Body.String(), "private/object-key")
	require.NotContains(t, recorder.Body.String(), "etag")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFileDeleteTargetRejectsMissingID(t *testing.T) {
	t.Parallel()
	handler := NewFileHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/files/delete-target", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.DeleteTarget(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestFileDeleteTargetHidesOtherTenantFiles(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT .* FROM files WHERE id=\? AND tenant_id=\? AND deleted_at IS NULL`).
		WithArgs("file-other", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "object_key", "original_name", "content_type", "size_bytes", "etag", "checksum_sha256", "created_at", "created_by", "updated_at", "updated_by", "version", "deleted_at", "deleted_by", "object_deleted_at", "object_delete_attempts", "object_delete_error", "object_delete_next_at"}))
	handler := NewFileHandler(files.New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, nil, config.Config{Files: config.Files{Enabled: true}}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "operator", Type: platformprincipal.TypeUser, TenantID: "tenant-a"})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/files/delete-target", strings.NewReader(`{"id":"file-other"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.DeleteTarget(c)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}
