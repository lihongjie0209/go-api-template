package files

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/objectstorage"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type unavailableFileActors struct{}

func (unavailableFileActors) ResolveUserIDs(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("identity unavailable")
}

func fileRows() *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows([]string{"id", "tenant_id", "object_key", "original_name", "content_type", "size_bytes", "etag", "checksum_sha256", "created_at", "created_by", "updated_at", "updated_by", "version", "deleted_at", "deleted_by", "object_deleted_at", "object_delete_attempts", "object_delete_error", "object_delete_next_at"}).
		AddRow("file-1", "tenant-1", "files/tenant-1/file-1/report.txt", "report.txt", "text/plain; charset=utf-8", 5, "etag-1", "checksum", now, "user-1", now, "user-1", 1, nil, nil, nil, 0, "", nil)
}

func TestUploadReturnsCommittedFileWhenActorDisplayServiceUnavailable(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO file_upload_intents`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO files`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE file_upload_intents SET status='completed'`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id, tenant_id, object_key`).WithArgs(sqlmock.AnyArg(), "tenant-1").WillReturnRows(fileRows())
	mock.ExpectCommit()
	storage := &storageStub{putInfo: objectstorage.Info{ETag: "etag-1"}}
	service := New(db, database.NewTransactor(db), storage, nil, operationStub{}, unavailableFileActors{}, nil, config.Config{Files: config.Files{Enabled: true, MaxSizeBytes: 1024}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	record, err := service.Upload(ctx, UploadInput{Name: "report.txt", Size: 5, Body: bytes.NewBufferString("hello")})
	require.NoError(t, err)
	require.Equal(t, "file-1", record.ID)
	require.Equal(t, "user-1", record.CreatedByName)
	require.Empty(t, storage.deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDownloadDoesNotRequireActorDisplayService(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(`SELECT id, tenant_id, object_key`).WithArgs("file-1", "tenant-1").WillReturnRows(fileRows())
	service := New(db, database.NewTransactor(db), &storageStub{}, nil, operationStub{}, unavailableFileActors{}, nil, config.Config{Files: config.Files{Enabled: true}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	result, err := service.Download(ctx, "file-1")
	require.NoError(t, err)
	require.Equal(t, "https://download.example/file", result.URL)
	require.Equal(t, "user-1", result.File.CreatedByName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteReportsVersionConflictDespiteActorDisplayServiceOutage(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	mock.ExpectQuery(`SELECT id, tenant_id, object_key`).WithArgs("file-1", "tenant-1").WillReturnRows(fileRows())
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE files SET deleted_at=`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	service := New(db, database.NewTransactor(db), &storageStub{}, nil, operationStub{}, unavailableFileActors{}, nil, config.Config{Files: config.Files{Enabled: true}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	err = service.Delete(ctx, "file-1", 1)
	require.ErrorIs(t, err, ErrVersionConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUploadResultReadFailureRollsBackAndRepairsObject(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO file_upload_intents`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO files`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE file_upload_intents SET status='completed'`).WillReturnResult(sqlmock.NewResult(0, 1))
	readFailure := errors.New("transactional result read failed")
	mock.ExpectQuery(`SELECT id, tenant_id, object_key`).WithArgs(sqlmock.AnyArg(), "tenant-1").WillReturnError(readFailure)
	mock.ExpectRollback()
	mock.ExpectQuery(`SELECT id,object_key,attempts FROM file_upload_intents`).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id", "object_key", "attempts"}).AddRow("file-1", "files/tenant-1/file-1/report.txt", 0))
	mock.ExpectQuery(`SELECT count\(\*\) FROM files`).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("file-upload-repair-worker").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE file_upload_intents SET status=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	storage := &storageStub{putInfo: objectstorage.Info{ETag: "etag-1"}}
	service := New(db, database.NewTransactor(db), storage, nil, operationStub{}, nil, nil, config.Config{Files: config.Files{Enabled: true, MaxSizeBytes: 1024}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	_, err = service.Upload(ctx, UploadInput{Name: "report.txt", Size: 5, Body: bytes.NewBufferString("hello")})
	require.ErrorIs(t, err, readFailure)
	require.Len(t, storage.deleted, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}
