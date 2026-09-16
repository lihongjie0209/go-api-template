package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
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

type actorResolverStub struct{}

func (actorResolverStub) ResolveUserIDs(context.Context, []string) (map[string]string, error) {
	return map[string]string{"user-1": "Alice", "service-1": "Billing Worker"}, nil
}

type storageStub struct {
	putInfo   objectstorage.Info
	putErr    error
	deleted   []string
	putType   string
	putBody   []byte
	putPath   string
	putSeeker bool
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
	_, s.putSeeker = input.Body.(io.Seeker)
	if file, ok := input.Body.(*os.File); ok {
		s.putPath = file.Name()
	}
	s.putBody, _ = io.ReadAll(input.Body)
	return s.putInfo, s.putErr
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

func TestStageUploadProducesSeekableFileAndRemovesIt(t *testing.T) {
	content := strings.Repeat("streamed-content-", 128)
	staged, err := stageUpload(t.Context(), strings.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	path := staged.file.Name()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("staged file stat: %v", err)
	}
	got, err := io.ReadAll(staged.file)
	if err != nil || string(got) != content {
		t.Fatalf("staged content length=%d err=%v", len(got), err)
	}
	if _, err := staged.file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	staged.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary upload still exists: %v", err)
	}
}

func TestStageUploadPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	staged, err := stageUpload(ctx, strings.NewReader("hello"), 5)
	if staged != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("staged=%v error=%v", staged, err)
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

func TestServiceUploadPersistsCleanupRetryWhenObjectStoreFails(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO file_upload_intents`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT id,object_key,attempts FROM file_upload_intents`).WithArgs(sqlmock.AnyArg()).WillReturnRows(
		sqlmock.NewRows([]string{"id", "object_key", "attempts"}).AddRow("file-1", "files/tenant-1/file-1/report.txt", 0),
	)
	mock.ExpectQuery(`SELECT count\(\*\) FROM files`).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("file-upload-repair-worker").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE file_upload_intents SET attempts=attempts\+1`).
		WithArgs("object storage unavailable", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	storage := &storageStub{putErr: errors.New("upload unavailable"), deleteErr: errors.New("object storage unavailable")}
	service := New(db, database.NewTransactor(db), storage, nil, operationStub{}, nil, slog.Default(), config.Config{Files: config.Files{Enabled: true, MaxSizeBytes: 1024, UploadStaleAfter: time.Minute, DeletionRetryDelay: time.Minute}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})

	_, err = service.Upload(ctx, UploadInput{Name: "report.txt", Size: 5, Body: bytes.NewBufferString("hello")})
	if err == nil || !strings.Contains(err.Error(), "upload unavailable") || len(storage.deleted) != 1 || !storage.putSeeker {
		t.Fatalf("error=%v deleted=%v put_seeker=%v", err, storage.deleted, storage.putSeeker)
	}
	if _, statErr := os.Stat(storage.putPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staged upload was not removed: %v", statErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessUploadIntentPreservesCompletedFileAfterAmbiguousCommit(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	mock.ExpectQuery(`SELECT id,object_key,attempts FROM file_upload_intents`).WithArgs("file-1").WillReturnRows(
		sqlmock.NewRows([]string{"id", "object_key", "attempts"}).AddRow("file-1", "files/tenant-1/file-1/report.txt", 0),
	)
	mock.ExpectQuery(`SELECT count\(\*\) FROM files`).WithArgs("file-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("file-upload-repair-worker").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE file_upload_intents SET status=`).WithArgs("completed", sqlmock.AnyArg(), "file-upload-repair-worker", "file-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	storage := &storageStub{}
	service := &Service{enabled: true, db: db, transactor: database.NewTransactor(db), storage: storage, cfg: config.Files{DeletionRetryDelay: time.Minute}}

	if err := service.processUploadIntent(t.Context(), "file-1", 0); err != nil {
		t.Fatal(err)
	}
	if len(storage.deleted) != 0 {
		t.Fatalf("completed object was deleted: %v", storage.deleted)
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
	mock.ExpectExec(`INSERT INTO file_upload_intents`).
		WithArgs(sqlmock.AnyArg(), "tenant-1", sqlmock.AnyArg(), "pending", 0, "", sqlmock.AnyArg(), sqlmock.AnyArg(), "user-1", sqlmock.AnyArg(), "user-1", 1).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO files .*object_delete_attempts, object_delete_error`).
		WithArgs(sqlmock.AnyArg(), "tenant-1", sqlmock.AnyArg(), "report.txt", "text/plain; charset=utf-8", int64(5), "etag-1", sqlmock.AnyArg(), sqlmock.AnyArg(), "user-1", sqlmock.AnyArg(), "user-1", 1, 0, "").
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()
	mock.ExpectQuery(`SELECT id,object_key,attempts FROM file_upload_intents`).WithArgs(sqlmock.AnyArg()).WillReturnRows(
		sqlmock.NewRows([]string{"id", "object_key", "attempts"}).AddRow("file-1", "ignored", 0),
	)
	// The generated ID is not known to the test, so use a callback-independent
	// row and accept the actual ID in both recovery queries.
	mock.ExpectQuery(`SELECT count\(\*\) FROM files`).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("file-upload-repair-worker").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE file_upload_intents SET status=`).WithArgs("abandoned", sqlmock.AnyArg(), "file-upload-repair-worker", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	storage := &storageStub{putInfo: objectstorage.Info{ETag: "etag-1"}}
	service := New(db, database.NewTransactor(db), storage, nil, operationStub{}, nil, slog.Default(), config.Config{Files: config.Files{Enabled: true, MaxSizeBytes: 1024}, ObjectStorage: config.ObjectStorage{PresignTTL: time.Minute}})
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
	mock.ExpectExec(`INSERT INTO file_upload_intents`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("user-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO files .*object_delete_attempts, object_delete_error`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE file_upload_intents SET status='completed'`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()
	mock.ExpectQuery(`SELECT id,object_key,attempts FROM file_upload_intents`).WithArgs(sqlmock.AnyArg()).WillReturnRows(
		sqlmock.NewRows([]string{"id", "object_key", "attempts"}).AddRow("file-1", "ignored", 0),
	)
	mock.ExpectQuery(`SELECT count\(\*\) FROM files`).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.actor_id', $1, true)`)).WithArgs("file-upload-repair-worker").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE file_upload_intents SET status=`).WithArgs("abandoned", sqlmock.AnyArg(), "file-upload-repair-worker", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	storage := &storageStub{putInfo: objectstorage.Info{ETag: "etag-1"}}
	operations := &failingTransactionalOperationStub{}
	service := New(db, database.NewTransactor(db), storage, nil, operations, nil, slog.Default(), config.Config{Files: config.Files{Enabled: true, MaxSizeBytes: 1024}, ObjectStorage: config.ObjectStorage{PresignTTL: time.Minute}})
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
	service := New(db, database.NewTransactor(db), &storageStub{}, nil, operationStub{}, nil, slog.Default(), config.Config{Files: config.Files{Enabled: true}})
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
	service := &Service{actors: actorResolverStub{}}
	instant := time.Date(2026, time.September, 16, 1, 2, 3, 0, time.UTC)
	records := []Record{{CreatedBy: "user-1", UpdatedBy: "service-1", CreatedAt: instant, UpdatedAt: instant}}
	if err := service.present(t.Context(), records); err != nil {
		t.Fatal(err)
	}
	if records[0].CreatedByName != "Alice" || records[0].UpdatedByName != "Billing Worker" || records[0].CreatedAt.Format(time.RFC3339) != "2026-09-16T09:02:03+08:00" || records[0].UpdatedAt.Format(time.RFC3339) != "2026-09-16T09:02:03+08:00" {
		t.Fatalf("record = %+v", records[0])
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
	operations := &failingReadOperationStub{}
	service := New(db, database.NewTransactor(db), &storageStub{}, nil, operations, nil, slog.Default(), config.Config{Files: config.Files{Enabled: true}, ObjectStorage: config.ObjectStorage{PresignTTL: time.Minute}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})

	download, err := service.Download(ctx, "file-1")
	if err == nil || download.URL != "" || len(operations.records) != 1 || !operations.records[0].Succeeded {
		t.Fatalf("download=%+v error=%v operation_records=%+v", download, err, operations.records)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
