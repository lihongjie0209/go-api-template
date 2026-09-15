package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/objectstorage"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type storageStub struct {
	putInfo   objectstorage.Info
	deleted   []string
	putType   string
	putBody   []byte
	deleteErr error
}
type operationStub struct{}

func (operationStub) Enabled() bool                                    { return true }
func (operationStub) Record(context.Context, operationlog.Entry) error { return nil }

func (s *storageStub) Put(_ context.Context, input objectstorage.PutInput) (objectstorage.Info, error) {
	s.putType = input.ContentType
	s.putBody, _ = io.ReadAll(input.Body)
	return s.putInfo, nil
}
func (s *storageStub) Get(context.Context, string) (*objectstorage.Object, error) { return nil, nil }
func (s *storageStub) Stat(context.Context, string) (objectstorage.Info, error) {
	return objectstorage.Info{}, nil
}
func (s *storageStub) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	return s.deleteErr
}

func TestServiceUploadRejectsDeclaredSizeMismatchBeforeStorage(t *testing.T) {
	storage := &storageStub{}
	service := &Service{enabled: true, db: &sqlx.DB{}, transactor: &database.Transactor{}, storage: storage, operations: operationStub{}, cfg: config.Files{MaxSizeBytes: 1024}}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", TenantID: "tenant-1"})
	_, err := service.Upload(ctx, UploadInput{Name: "report.txt", Size: 4, Body: bytes.NewBufferString("hello")})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Upload() error=%v, want ErrInvalidInput", err)
	}
	if len(storage.putBody) != 0 {
		t.Fatal("storage Put was called for a size-mismatched upload")
	}
}

func TestProcessDeletionPersistsRetryAfterStorageFailure(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	storage := &storageStub{deleteErr: errors.New("object storage unavailable")}
	service := &Service{enabled: true, db: db, transactor: database.NewTransactor(db), storage: storage, cfg: config.Files{DeletionRetryDelay: time.Minute}}
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("file-deletion-worker").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE files SET object_delete_attempts=object_delete_attempts\+1`).
		WithArgs("object storage unavailable", sqlmock.AnyArg(), sqlmock.AnyArg(), "file-deletion-worker", "file-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err = service.processDeletion(t.Context(), "file-1", "files/tenant/file-1/report.txt", 0)
	if err == nil || !strings.Contains(err.Error(), "object storage unavailable") {
		t.Fatalf("processDeletion() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func (s *storageStub) Presign(context.Context, string, objectstorage.Operation, time.Duration) (objectstorage.SignedURL, error) {
	return objectstorage.SignedURL{URL: "https://download.example/file", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func TestService_UploadCompensatesWhenDatabaseInsertFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO files`).WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()
	storage := &storageStub{putInfo: objectstorage.Info{ETag: "etag-1"}}
	service := New(db, database.NewTransactor(db), storage, nil, operationStub{}, slog.Default(), config.Config{Files: config.Files{Enabled: true, MaxSizeBytes: 1024}, ObjectStorage: config.ObjectStorage{PresignTTL: time.Minute}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})

	_, err = service.Upload(ctx, UploadInput{Name: "../report.txt", ContentType: "text/plain", Size: 5, Body: bytes.NewBufferString("hello")})
	if err == nil {
		t.Fatal("Upload() error = nil")
	}
	if len(storage.deleted) != 1 {
		t.Fatalf("compensation deletes = %v", storage.deleted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_GetEnforcesTenant(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	mock.ExpectQuery(`SELECT id, tenant_id`).WithArgs("file-1", "tenant-b").WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "object_key", "original_name", "content_type", "size_bytes", "etag", "checksum_sha256", "created_at", "created_by", "updated_at", "updated_by", "version", "deleted_at", "deleted_by"}))
	service := New(db, database.NewTransactor(db), &storageStub{}, nil, operationStub{}, slog.Default(), config.Config{Files: config.Files{Enabled: true}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-b", Type: platformprincipal.TypeUser, TenantID: "tenant-b"})

	_, err = service.Get(ctx, "file-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
