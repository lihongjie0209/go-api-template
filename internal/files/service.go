package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/objectstorage"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrDisabled        = errors.New("file service is disabled")
	ErrNotFound        = errors.New("file not found")
	ErrForbidden       = errors.New("file access denied")
	ErrVersionConflict = errors.New("file version conflict")
	ErrInvalidInput    = errors.New("invalid file input")
)

var safeObjectSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Record struct {
	ID                   string     `db:"id" json:"id"`
	TenantID             string     `db:"tenant_id" json:"tenant_id"`
	ObjectKey            string     `db:"object_key" json:"-"`
	OriginalName         string     `db:"original_name" json:"original_name"`
	ContentType          string     `db:"content_type" json:"content_type"`
	SizeBytes            int64      `db:"size_bytes" json:"size_bytes"`
	ETag                 string     `db:"etag" json:"etag"`
	ChecksumSHA256       string     `db:"checksum_sha256" json:"checksum_sha256"`
	CreatedAt            time.Time  `db:"created_at" json:"created_at"`
	CreatedBy            string     `db:"created_by" json:"created_by"`
	UpdatedAt            time.Time  `db:"updated_at" json:"updated_at"`
	UpdatedBy            string     `db:"updated_by" json:"updated_by"`
	Version              int64      `db:"version" json:"version"`
	DeletedAt            *time.Time `db:"deleted_at" json:"-"`
	DeletedBy            *string    `db:"deleted_by" json:"-"`
	ObjectDeletedAt      *time.Time `db:"object_deleted_at" json:"-"`
	ObjectDeleteAttempts int64      `db:"object_delete_attempts" json:"-"`
	ObjectDeleteError    string     `db:"object_delete_error" json:"-"`
	ObjectDeleteNextAt   *time.Time `db:"object_delete_next_at" json:"-"`
}

type UploadInput struct {
	Name        string
	ContentType string
	Size        int64
	Body        io.Reader
}

type Download struct {
	File      Record    `json:"file"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type PageInput struct {
	pagination.Request
	Keyword       string
	IDs           []string
	ContentTypes  []string
	CreatedByIDs  []string
	SizeFrom      *int64
	SizeTo        *int64
	CreatedAtFrom *time.Time
	CreatedAtTo   *time.Time
}

type Page struct {
	Items    []Record `json:"items"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Total    int64    `json:"total"`
}

type Service struct {
	enabled    bool
	db         *sqlx.DB
	transactor *database.Transactor
	storage    objectstorage.Store
	locker     cache.Locker
	operations operationlog.TransactionalRecorder
	logger     *slog.Logger
	cfg        config.Files
	lockTTL    time.Duration
	lockRetry  time.Duration
}

func New(db *sqlx.DB, transactor *database.Transactor, storage objectstorage.Store, locker cache.Locker, operations operationlog.TransactionalRecorder, logger *slog.Logger, cfg config.Config) *Service {
	return &Service{enabled: cfg.Files.Enabled, db: db, transactor: transactor, storage: storage, locker: locker, operations: operations, logger: logger, cfg: cfg.Files, lockTTL: cfg.DistributedLock.TTL, lockRetry: cfg.DistributedLock.RetryDelay}
}

const recordColumns = `id, tenant_id, object_key, original_name, content_type, size_bytes, etag, checksum_sha256, created_at, created_by, updated_at, updated_by, version, deleted_at, deleted_by, object_deleted_at, object_delete_attempts, object_delete_error, object_delete_next_at`

func (s *Service) Upload(ctx context.Context, input UploadInput) (Record, error) {
	actor, err := actor(ctx)
	if err != nil {
		return Record{}, err
	}
	if err := s.validateUpload(input); err != nil {
		return Record{}, err
	}
	name := path.Base(strings.ReplaceAll(input.Name, "\\", "/"))
	content, err := io.ReadAll(io.LimitReader(input.Body, input.Size+1))
	if err != nil {
		return Record{}, fmt.Errorf("%w: read upload: %v", ErrInvalidInput, err)
	}
	if int64(len(content)) != input.Size {
		return Record{}, fmt.Errorf("%w: declared size %d does not match content size %d", ErrInvalidInput, input.Size, len(content))
	}
	detectedType := http.DetectContentType(content)
	if err := s.validateContentType(detectedType); err != nil {
		return Record{}, err
	}
	id := uuid.NewString()
	objectKey := fmt.Sprintf("files/%s/%s/%s", tenantSegment(actor.TenantID), id, name)
	checksum := sha256.Sum256(content)
	record := Record{ID: id, TenantID: actor.TenantID, ObjectKey: objectKey, OriginalName: name, ContentType: detectedType, SizeBytes: input.Size, ChecksumSHA256: hex.EncodeToString(checksum[:])}
	request := map[string]any{"name": name, "content_type": detectedType, "size_bytes": input.Size, "checksum_sha256": record.ChecksumSHA256}
	entry := operationlog.Entry{Operation: "file.upload", ResourceType: "file", ResourceID: id, Source: "backend", Protocol: "service", Request: request}
	started := time.Now()
	stored, err := s.storage.Put(ctx, objectstorage.PutInput{Key: objectKey, Body: bytes.NewReader(content), Size: input.Size, ContentType: detectedType, Metadata: map[string]string{"sha256": record.ChecksumSHA256}})
	if err != nil {
		s.recordFailure(ctx, entry, started)
		return Record{}, fmt.Errorf("upload object: %w", err)
	}
	record.ETag = stored.ETag
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		if err := insert(ctx, tx, record, actor.ID); err != nil {
			return err
		}
		entry.Duration = time.Since(started)
		entry.Succeeded = true
		return s.operations.RecordTx(ctx, tx, entry)
	})
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if cleanupErr := s.storage.Delete(cleanupCtx, objectKey); cleanupErr != nil && s.logger != nil {
			s.logger.ErrorContext(cleanupCtx, "cleanup failed uploaded object", "file_id", id, "error", cleanupErr)
		}
		s.recordFailure(ctx, entry, started)
		return Record{}, fmt.Errorf("record uploaded file: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *Service) Page(ctx context.Context, input PageInput) (Page, error) {
	if !s.enabled {
		return Page{}, ErrDisabled
	}
	actor, err := actor(ctx)
	if err != nil {
		return Page{}, err
	}
	request, err := pagination.Normalize(input.Request)
	if err != nil || invalidPageInput(input) {
		return Page{}, ErrInvalidInput
	}
	where, args := fileActorScope(actor)
	where += ` AND deleted_at IS NULL`
	if keyword := strings.ToLower(strings.TrimSpace(input.Keyword)); keyword != "" {
		where += ` AND LOWER(original_name) LIKE ?`
		args = append(args, "%"+keyword+"%")
	}
	for _, filter := range []struct {
		column string
		values []string
	}{{"id", input.IDs}, {"content_type", input.ContentTypes}, {"created_by", input.CreatedByIDs}} {
		if len(filter.values) == 0 {
			continue
		}
		clause, inArgs, inErr := sqlx.In(filter.column+` IN (?)`, filter.values)
		if inErr != nil {
			return Page{}, ErrInvalidInput
		}
		where += " AND " + clause
		args = append(args, inArgs...)
	}
	if input.SizeFrom != nil {
		where += ` AND size_bytes>=?`
		args = append(args, *input.SizeFrom)
	}
	if input.SizeTo != nil {
		where += ` AND size_bytes<=?`
		args = append(args, *input.SizeTo)
	}
	if input.CreatedAtFrom != nil {
		where += ` AND created_at>=?`
		args = append(args, *input.CreatedAtFrom)
	}
	if input.CreatedAtTo != nil {
		where += ` AND created_at<?`
		args = append(args, *input.CreatedAtTo)
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM files WHERE `+where), args...); err != nil {
		return Page{}, fmt.Errorf("count files: %w", err)
	}
	items := []Record{}
	queryArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	query := `SELECT ` + recordColumns + ` FROM files WHERE ` + where + ` ORDER BY created_at DESC,id LIMIT ? OFFSET ?`
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(query), queryArgs...); err != nil {
		return Page{}, fmt.Errorf("page files: %w", err)
	}
	return Page{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	if !s.enabled {
		return Record{}, ErrDisabled
	}
	actor, err := actor(ctx)
	if err != nil {
		return Record{}, err
	}
	if strings.TrimSpace(id) == "" || len(id) > 256 {
		return Record{}, ErrInvalidInput
	}
	var record Record
	where, args := fileScope(actor, id)
	query := s.db.Rebind(`SELECT ` + recordColumns + ` FROM files WHERE ` + where + ` AND deleted_at IS NULL`)
	if err := s.db.GetContext(ctx, &record, query, args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, fmt.Errorf("get file: %w", err)
	}
	return record, nil
}

func (s *Service) Download(ctx context.Context, id string) (Download, error) {
	started := time.Now()
	entry := operationlog.Entry{Operation: "file.download", ResourceType: "file", ResourceID: id, Source: "backend", Protocol: "service", Request: map[string]any{"id": id}}
	record, err := s.Get(ctx, id)
	if err != nil {
		s.recordFailure(ctx, entry, started)
		return Download{}, err
	}
	signed, err := s.storage.Presign(ctx, record.ObjectKey, objectstorage.OperationGet, 0)
	if err != nil {
		s.recordFailure(ctx, entry, started)
		return Download{}, fmt.Errorf("presign file download: %w", err)
	}
	entry.Duration = time.Since(started)
	entry.Succeeded = true
	if err := s.operations.Record(ctx, entry); err != nil {
		return Download{}, fmt.Errorf("record file download: %w", err)
	}
	return Download{File: record, URL: signed.URL, ExpiresAt: signed.ExpiresAt}, nil
}

func (s *Service) Delete(ctx context.Context, id string, version int64) error {
	if !s.enabled {
		return ErrDisabled
	}
	actorValue, err := actor(ctx)
	if err != nil {
		return err
	}
	record, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if version <= 0 {
		return fmt.Errorf("%w: version must be positive", ErrInvalidInput)
	}
	request := map[string]any{"version": version, "original_name": record.OriginalName}
	entry := operationlog.Entry{Operation: "file.delete", ResourceType: "file", ResourceID: id, Source: "backend", Protocol: "service", Request: request}
	started := time.Now()
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		where, args := fileScope(actorValue, id)
		query := tx.Rebind(`UPDATE files SET deleted_at=?,deleted_by=?,object_delete_next_at=?,object_delete_error='',updated_at=?,updated_by=?,version=version+1 WHERE ` + where + ` AND version=? AND deleted_at IS NULL`)
		now := time.Now()
		updateArgs := append([]any{now, actorValue.ID, now, now, actorValue.ID}, args...)
		updateArgs = append(updateArgs, version)
		result, execErr := tx.ExecContext(ctx, query, updateArgs...)
		if execErr != nil {
			return execErr
		}
		rows, execErr := result.RowsAffected()
		if execErr != nil {
			return execErr
		}
		if rows != 1 {
			return ErrVersionConflict
		}
		entry.Duration = time.Since(started)
		entry.Succeeded = true
		return s.operations.RecordTx(ctx, tx, entry)
	})
	if err != nil {
		s.recordFailure(ctx, entry, started)
		return fmt.Errorf("soft delete file: %w", err)
	}
	if err := s.withDeletionLock(ctx, record.ID, func(lockCtx context.Context) error { return s.processDeletion(lockCtx, record.ID, record.ObjectKey, 0) }); err != nil && s.logger != nil {
		s.logger.WarnContext(ctx, "file object deletion queued for retry", "file_id", record.ID, "error", err)
	}
	return nil
}

func (s *Service) recordFailure(ctx context.Context, entry operationlog.Entry, started time.Time) {
	entry.Duration = time.Since(started)
	entry.Succeeded = false
	entry.ErrorCode = "operation_failed"
	entry.ErrorMessage = "operation failed"
	if err := s.operations.Record(ctx, entry); err != nil && s.logger != nil {
		s.logger.ErrorContext(ctx, "record failed file operation", "operation", entry.Operation, "file_id", entry.ResourceID, "error", err)
	}
}

type pendingDeletion struct {
	ID        string `db:"id"`
	ObjectKey string `db:"object_key"`
	Attempts  int64  `db:"object_delete_attempts"`
}

// ProcessPendingDeletions retries object deletion for logically deleted files.
// The row remains the durable queue; per-file distributed locks prevent two
// replicas from deleting and updating the same item concurrently.
func (s *Service) ProcessPendingDeletions(ctx context.Context) error {
	if !s.enabled {
		return nil
	}
	items := []pendingDeletion{}
	query := s.db.Rebind(`SELECT id,object_key,object_delete_attempts FROM files WHERE deleted_at IS NOT NULL AND object_deleted_at IS NULL AND (object_delete_next_at IS NULL OR object_delete_next_at<=?) ORDER BY object_delete_next_at,id LIMIT ?`)
	if err := s.db.SelectContext(ctx, &items, query, time.Now(), s.cfg.DeletionBatchSize); err != nil {
		return fmt.Errorf("list pending file object deletions: %w", err)
	}
	var result error
	for _, item := range items {
		if err := s.withDeletionLock(ctx, item.ID, func(lockCtx context.Context) error {
			return s.processDeletion(lockCtx, item.ID, item.ObjectKey, item.Attempts)
		}); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *Service) RunDeletionWorker(ctx context.Context) {
	if !s.enabled {
		return
	}
	ticker := time.NewTicker(s.cfg.DeletionInterval)
	defer ticker.Stop()
	for {
		if err := s.ProcessPendingDeletions(ctx); err != nil && !errors.Is(err, context.Canceled) && s.logger != nil {
			s.logger.WarnContext(ctx, "process pending file object deletions", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) processDeletion(ctx context.Context, id, objectKey string, attempts int64) error {
	deleteErr := s.storage.Delete(ctx, objectKey)
	workerBase, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	workerCtx := platformprincipal.SystemContext(workerBase, "file-deletion-worker")
	txErr := s.transactor.Within(workerCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		if deleteErr == nil {
			result, err := tx.ExecContext(workerCtx, tx.Rebind(`UPDATE files SET object_deleted_at=?,object_delete_error='',object_delete_next_at=NULL,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND deleted_at IS NOT NULL AND object_deleted_at IS NULL`), now, now, "file-deletion-worker", id)
			if err != nil {
				return err
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if rows > 1 {
				return fmt.Errorf("complete file deletion affected %d rows", rows)
			}
			return nil
		}
		next := now.Add(deletionBackoff(s.cfg.DeletionRetryDelay, attempts))
		message := deleteErr.Error()
		if len(message) > 2000 {
			message = message[:2000]
		}
		result, updateErr := tx.ExecContext(workerCtx, tx.Rebind(`UPDATE files SET object_delete_attempts=object_delete_attempts+1,object_delete_error=?,object_delete_next_at=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND deleted_at IS NOT NULL AND object_deleted_at IS NULL`), message, next, now, "file-deletion-worker", id)
		if updateErr != nil {
			return updateErr
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if rows > 1 {
			return fmt.Errorf("reschedule file deletion affected %d rows", rows)
		}
		return nil
	})
	return errors.Join(deleteErr, txErr)
}

func deletionBackoff(base time.Duration, attempts int64) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > 6 {
		attempts = 6
	}
	return base * time.Duration(1<<attempts)
}

func (s *Service) withDeletionLock(ctx context.Context, id string, fn func(context.Context) error) error {
	if s.locker == nil {
		// Object deletion and the conditional database update are idempotent;
		// the lock only suppresses duplicate work during tests or degraded repair.
		return fn(ctx)
	}
	return cache.WithLock(ctx, s.locker, "file:deletion:"+id, s.lockTTL, s.lockRetry, fn)
}

func (s *Service) validateUpload(input UploadInput) error {
	if !s.enabled || s.db == nil || s.storage == nil || s.transactor == nil {
		return ErrDisabled
	}
	if input.Body == nil || input.Size < 0 || input.Size > s.cfg.MaxSizeBytes {
		return fmt.Errorf("%w: invalid file size", ErrInvalidInput)
	}
	name := path.Base(strings.ReplaceAll(input.Name, "\\", "/"))
	if strings.TrimSpace(input.Name) == "" || name == "." || name == "/" || len(name) > 255 || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: file name is required", ErrInvalidInput)
	}
	return nil
}

func (s *Service) validateContentType(contentType string) error {
	if len(s.cfg.AllowedTypes) > 0 {
		for _, allowed := range s.cfg.AllowedTypes {
			if strings.EqualFold(strings.TrimSpace(contentType), strings.TrimSpace(allowed)) {
				return nil
			}
		}
		return fmt.Errorf("%w: detected content type %q is not allowed", ErrInvalidInput, contentType)
	}
	return nil
}

func insert(ctx context.Context, tx *sqlx.Tx, record Record, actorID string) error {
	now := time.Now()
	query := tx.Rebind(`INSERT INTO files (id, tenant_id, object_key, original_name, content_type, size_bytes, etag, checksum_sha256, created_at, created_by, updated_at, updated_by, version, object_delete_attempts, object_delete_error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := tx.ExecContext(ctx, query, record.ID, record.TenantID, record.ObjectKey, record.OriginalName, record.ContentType, record.SizeBytes, record.ETag, record.ChecksumSHA256, now, actorID, now, actorID, 1, 0, "")
	return err
}

func actor(ctx context.Context) (platformprincipal.Principal, error) {
	value, err := platformprincipal.Require(ctx)
	if err != nil {
		return platformprincipal.Principal{}, err
	}
	if len(value.ID) > 256 || (value.TenantID != "" && !safeObjectSegment.MatchString(value.TenantID)) {
		return platformprincipal.Principal{}, ErrForbidden
	}
	return value, nil
}

func invalidPageInput(input PageInput) bool {
	if len(input.Keyword) > 256 || len(input.IDs) > 200 || len(input.ContentTypes) > 100 || len(input.CreatedByIDs) > 200 ||
		(input.SizeFrom != nil && *input.SizeFrom < 0) || (input.SizeTo != nil && *input.SizeTo < 0) ||
		(input.SizeFrom != nil && input.SizeTo != nil && *input.SizeFrom > *input.SizeTo) ||
		(input.CreatedAtFrom != nil && input.CreatedAtTo != nil && !input.CreatedAtFrom.Before(*input.CreatedAtTo)) {
		return true
	}
	for _, values := range [][]string{input.IDs, input.ContentTypes, input.CreatedByIDs} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" || len(value) > 256 || !utf8.ValidString(value) {
				return true
			}
		}
	}
	return false
}
func tenantSegment(value string) string {
	if value == "" {
		return "platform"
	}
	return value
}
func fileScope(actor platformprincipal.Principal, id string) (string, []any) {
	where, args := fileActorScope(actor)
	return `id=? AND ` + where, append([]any{id}, args...)
}
func fileActorScope(actor platformprincipal.Principal) (string, []any) {
	if actor.TenantID != "" {
		return `tenant_id=?`, []any{actor.TenantID}
	}
	return `tenant_id='' AND created_by=?`, []any{actor.ID}
}
