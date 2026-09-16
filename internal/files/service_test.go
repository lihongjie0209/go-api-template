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

type failingTransactionalOperationStub struct{ records []operationlog.Entry }
type failingReadOperationStub struct{ records []operationlog.Entry }

func (operationStub) Enabled() bool                                    { return true }
func (operationStub) Record(context.Context, operationlog.Entry) error { return nil }
func (operationStub) RecordTx(context.Context, *sqlx.Tx, operationlog.Entry) error {
	return nil
}
func (s *failingTransactionalOperationStub) Enabled() bool { return true }
func (s *failingTransactionalOperationStub) Record(_ context.Context, entry operationlog.Entry) error {
	s.records = append(s.records, entry)
	return nil
}
func (*failingTransactionalOperationStub) RecordTx(context.Context, *sqlx.Tx, operationlog.Entry) error {
	return errors.New("operation outbox unavailable")
}
func (*failingReadOperationStub) Enabled() bool { return true }
func (s *failingReadOperationStub) Record(_ context.Context, entry operationlog.Entry) error {
	s.records = append(s.records, entry)
	return errors.New("operation outbox unavailable")
}
func (*failingReadOperationStub) RecordTx(context.Context, *sqlx.Tx, operationlog.Entry) error {
	return nil
}

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
	mock.ExpectExec(`INSERT INTO files .*object_delete_attempts, object_delete_error`).
		WithArgs(sqlmock.AnyArg(), "tenant-1", sqlmock.AnyArg(), "report.txt", "text/plain; charset=utf-8", int64(5), "etag-1", sqlmock.AnyArg(), sqlmock.AnyArg(), "user-1", sqlmock.AnyArg(), "user-1", 1, 0, "").
		WillReturnError(errors.New("database unavailable"))
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

func TestServiceUploadRollsBackMetadataWhenTransactionalLogFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO files .*object_delete_attempts, object_delete_error`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectRollback()
	storage := &storageStub{putInfo: objectstorage.Info{ETag: "etag-1"}}
	operations := &failingTransactionalOperationStub{}
	service := New(db, database.NewTransactor(db), storage, nil, operations, slog.Default(), config.Config{Files: config.Files{Enabled: true, MaxSizeBytes: 1024}, ObjectStorage: config.ObjectStorage{PresignTTL: time.Minute}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})

	_, err = service.Upload(ctx, UploadInput{Name: "report.txt", Size: 5, Body: bytes.NewBufferString("hello")})
	if err == nil || len(storage.deleted) != 1 || len(operations.records) != 1 || operations.records[0].Succeeded {
		t.Fatalf("error=%v deletes=%v operation_records=%+v", err, storage.deleted, operations.records)
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

func TestServicePresentResolvesAuditNamesAndAsiaShanghaiTimes(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(`SELECT id,display_name FROM identity_users`).WithArgs("user-1", "service-1", "user-1", "service-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "display_name"}).AddRow("user-1", "Alice").AddRow("service-1", "Billing Worker"))
	service := &Service{db: db}
	instant := time.Date(2026, time.September, 16, 1, 2, 3, 0, time.UTC)
	records := []Record{{CreatedBy: "user-1", UpdatedBy: "service-1", CreatedAt: instant, UpdatedAt: instant}}
	if err := service.present(t.Context(), records); err != nil {
		t.Fatal(err)
	}
	if records[0].CreatedByName != "Alice" || records[0].UpdatedByName != "Billing Worker" || records[0].CreatedAt.Format(time.RFC3339) != "2026-09-16T09:02:03+08:00" || records[0].UpdatedAt.Format(time.RFC3339) != "2026-09-16T09:02:03+08:00" {
		t.Fatalf("record = %+v", records[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceDownloadFailsClosedWhenAccessLogCannotBeStored(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	now := time.Now()
	columns := strings.Split(strings.ReplaceAll(recordColumns, " ", ""), ",")
	mock.ExpectQuery(`SELECT id, tenant_id`).WithArgs("file-1", "tenant-1").WillReturnRows(sqlmock.NewRows(columns).AddRow(
		"file-1", "tenant-1", "files/tenant-1/file-1/report.txt", "report.txt", "text/plain", int64(5), "etag", "checksum",
		now, "user-1", now, "user-1", int64(1), nil, nil, nil, int64(0), "", nil,
	))
	mock.ExpectQuery(`SELECT id,display_name FROM identity_users`).WithArgs("user-1", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "display_name"}).AddRow("user-1", "Alice"))
	operations := &failingReadOperationStub{}
	service := New(db, database.NewTransactor(db), &storageStub{}, nil, operations, slog.Default(), config.Config{Files: config.Files{Enabled: true}, ObjectStorage: config.ObjectStorage{PresignTTL: time.Minute}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})

	download, err := service.Download(ctx, "file-1")
	if err == nil || download.URL != "" || len(operations.records) != 1 || !operations.records[0].Succeeded {
		t.Fatalf("download=%+v error=%v operation_records=%+v", download, err, operations.records)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
