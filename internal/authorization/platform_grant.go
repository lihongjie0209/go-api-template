package authorization

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrPlatformGrantInvalid      = errors.New("invalid platform grant")
	ErrPlatformGrantNotFound     = errors.New("platform grant not found")
	ErrPlatformGrantConflict     = errors.New("platform grant conflict")
	ErrPlatformGrantStaleVersion = errors.New("platform grant stale version")
)

type PlatformGrant struct {
	ID                  string    `db:"id" json:"id"`
	PrincipalType       string    `db:"principal_type" json:"principal_type"`
	PrincipalID         string    `db:"principal_id" json:"principal_id"`
	PrincipalName       string    `db:"principal_name" json:"principal_name"`
	PrincipalIdentifier string    `db:"principal_identifier" json:"principal_identifier"`
	PermissionID        string    `db:"permission_id" json:"permission_id"`
	PermissionKey       string    `db:"permission_key" json:"permission_key"`
	PermissionName      string    `db:"name" json:"permission_name"`
	CreatedAt           time.Time `db:"created_at" json:"created_at"`
	CreatedBy           string    `db:"created_by" json:"created_by"`
	UpdatedAt           time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy           string    `db:"updated_by" json:"updated_by"`
	Version             int64     `db:"version" json:"version"`
}

type PlatformGrantInput struct{ PrincipalType, PrincipalID, PermissionID string }
type PlatformGrantUpdateTarget struct {
	ID                  string `db:"id" json:"id"`
	PrincipalType       string `db:"principal_type" json:"principal_type"`
	PrincipalID         string `db:"principal_id" json:"principal_id"`
	PrincipalName       string `db:"principal_name" json:"principal_name"`
	PrincipalIdentifier string `db:"principal_identifier" json:"principal_identifier"`
	PermissionID        string `db:"permission_id" json:"permission_id"`
	PermissionKey       string `db:"permission_key" json:"permission_key"`
	PermissionName      string `db:"permission_name" json:"permission_name"`
	Version             int64  `db:"version" json:"version"`
}
type PlatformGrantDeleteTarget struct {
	ID                  string `db:"id" json:"id"`
	PrincipalType       string `db:"principal_type" json:"principal_type"`
	PrincipalName       string `db:"principal_name" json:"principal_name"`
	PrincipalIdentifier string `db:"principal_identifier" json:"principal_identifier"`
	PermissionKey       string `db:"permission_key" json:"permission_key"`
	PermissionName      string `db:"permission_name" json:"permission_name"`
	Version             int64  `db:"version" json:"version"`
}
type PlatformGrantPageInput struct {
	pagination.Request
	PrincipalType, PrincipalID string
	PermissionIDs              []string
}
type PlatformGrantPage struct {
	Items    []PlatformGrant `json:"items"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	Total    int64           `json:"total"`
}
type PlatformGrantablePermission struct {
	ID   string `db:"id" json:"id"`
	Key  string `db:"permission_key" json:"permission_key"`
	Name string `db:"name" json:"name"`
}
type PlatformGrantablePermissionPage struct {
	Items    []PlatformGrantablePermission `json:"items"`
	Page     int                           `json:"page"`
	PageSize int                           `json:"page_size"`
	Total    int64                         `json:"total"`
}
type PlatformGrantablePermissionPageInput struct {
	pagination.Request
	IDs []string
}
type PlatformGrantService struct {
	db         *sqlx.DB
	tx         *database.Transactor
	operations operationlog.TransactionalRecorder
	security   securitylog.TransactionalRecorder
}

func NewPlatformGrantService(db *sqlx.DB, tx *database.Transactor, operations operationlog.TransactionalRecorder, security securitylog.TransactionalRecorder) *PlatformGrantService {
	return &PlatformGrantService{db: db, tx: tx, operations: operations, security: security}
}

const platformGrantSelect = `SELECT g.id,g.principal_type,g.principal_id,COALESCE(u.display_name,sa.name,g.principal_id) AS principal_name,COALESCE(u.username,sa.client_id,g.principal_id) AS principal_identifier,g.permission_id,p.permission_key,p.name,g.created_at,g.created_by,g.updated_at,g.updated_by,g.version FROM platform_principal_permissions g JOIN permissions p ON p.id=g.permission_id LEFT JOIN identity_users u ON g.principal_type='user' AND u.id=g.principal_id AND u.deleted_at IS NULL LEFT JOIN identity_service_accounts sa ON g.principal_type='service_account' AND sa.id=g.principal_id AND sa.deleted_at IS NULL`

const platformGrantTargetSource = ` FROM platform_principal_permissions g JOIN permissions p ON p.id=g.permission_id AND p.deleted_at IS NULL LEFT JOIN identity_users u ON g.principal_type='user' AND u.id=g.principal_id AND u.deleted_at IS NULL LEFT JOIN identity_service_accounts sa ON g.principal_type='service_account' AND sa.id=g.principal_id AND sa.deleted_at IS NULL WHERE g.id=? AND g.deleted_at IS NULL`

func (s *PlatformGrantService) UpdateTarget(ctx context.Context, id string) (PlatformGrantUpdateTarget, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return PlatformGrantUpdateTarget{}, err
	}
	if _, err := uuid.Parse(id); err != nil {
		return PlatformGrantUpdateTarget{}, ErrPlatformGrantInvalid
	}
	const fields = `SELECT g.id,g.principal_type,g.principal_id,COALESCE(NULLIF(u.display_name,''),NULLIF(sa.name,''),g.principal_id) AS principal_name,COALESCE(NULLIF(u.username,''),NULLIF(sa.client_id,''),g.principal_id) AS principal_identifier,g.permission_id,p.permission_key,p.name AS permission_name,g.version`
	var target PlatformGrantUpdateTarget
	err := s.db.GetContext(ctx, &target, s.db.Rebind(fields+platformGrantTargetSource), id)
	if errors.Is(err, sql.ErrNoRows) {
		return PlatformGrantUpdateTarget{}, ErrPlatformGrantNotFound
	}
	if err != nil {
		return PlatformGrantUpdateTarget{}, fmt.Errorf("read platform grant update target: %w", err)
	}
	return target, nil
}

func (s *PlatformGrantService) DeleteTarget(ctx context.Context, id string) (PlatformGrantDeleteTarget, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return PlatformGrantDeleteTarget{}, err
	}
	if _, err := uuid.Parse(id); err != nil {
		return PlatformGrantDeleteTarget{}, ErrPlatformGrantInvalid
	}
	const fields = `SELECT g.id,g.principal_type,COALESCE(NULLIF(u.display_name,''),NULLIF(sa.name,''),g.principal_id) AS principal_name,COALESCE(NULLIF(u.username,''),NULLIF(sa.client_id,''),g.principal_id) AS principal_identifier,p.permission_key,p.name AS permission_name,g.version`
	var target PlatformGrantDeleteTarget
	err := s.db.GetContext(ctx, &target, s.db.Rebind(fields+platformGrantTargetSource), id)
	if errors.Is(err, sql.ErrNoRows) {
		return PlatformGrantDeleteTarget{}, ErrPlatformGrantNotFound
	}
	if err != nil {
		return PlatformGrantDeleteTarget{}, fmt.Errorf("read platform grant delete target: %w", err)
	}
	return target, nil
}

const platformGrantablePermissionWhere = `p.node_type='permission' AND p.status='active' AND p.deleted_at IS NULL AND EXISTS (
	SELECT 1 FROM permission_resource_actions l
	JOIN authorization_resource_actions a ON a.id=l.action_id AND a.status='active' AND a.deleted_at IS NULL
	JOIN authorization_resources r ON r.id=a.resource_id AND r.scope='platform' AND r.status='active' AND r.deleted_at IS NULL
	WHERE l.permission_id=p.id AND l.deleted_at IS NULL
)`

func (s *PlatformGrantService) PageGrantablePermissions(ctx context.Context, input PlatformGrantablePermissionPageInput) (PlatformGrantablePermissionPage, error) {
	request, err := pagination.Normalize(input.Request)
	if err != nil || len(input.Keyword) > 256 || len(input.IDs) > 200 {
		return PlatformGrantablePermissionPage{}, ErrPlatformGrantInvalid
	}
	where := platformGrantablePermissionWhere
	args := []any{}
	if len(input.IDs) > 0 {
		for _, id := range input.IDs {
			if _, err := uuid.Parse(id); err != nil {
				return PlatformGrantablePermissionPage{}, ErrPlatformGrantInvalid
			}
		}
		inQuery, inArgs, err := sqlx.In(`p.id IN (?)`, input.IDs)
		if err != nil {
			return PlatformGrantablePermissionPage{}, fmt.Errorf("build platform-grantable permission ID filter: %w", err)
		}
		where += ` AND ` + inQuery
		args = append(args, inArgs...)
	}
	if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
		where += ` AND (LOWER(p.permission_key) LIKE LOWER(?) ESCAPE '!' OR LOWER(p.name) LIKE LOWER(?) ESCAPE '!')`
		like := database.LikeContainsPattern(keyword)
		args = append(args, like, like)
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM permissions p WHERE `+where), args...); err != nil {
		return PlatformGrantablePermissionPage{}, fmt.Errorf("count platform-grantable permissions: %w", err)
	}
	items := []PlatformGrantablePermission{}
	pageArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	query := `SELECT p.id,p.permission_key,p.name FROM permissions p WHERE ` + where + ` ORDER BY p.permission_key,p.id LIMIT ? OFFSET ?`
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(query), pageArgs...); err != nil {
		return PlatformGrantablePermissionPage{}, fmt.Errorf("list platform-grantable permissions: %w", err)
	}
	return PlatformGrantablePermissionPage{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func validatePlatformGrant(input PlatformGrantInput) error {
	if input.PrincipalType != "user" && input.PrincipalType != "service_account" {
		return ErrPlatformGrantInvalid
	}
	if _, err := uuid.Parse(input.PrincipalID); err != nil {
		return ErrPlatformGrantInvalid
	}
	if _, err := uuid.Parse(input.PermissionID); err != nil {
		return ErrPlatformGrantInvalid
	}
	return nil
}

func (s *PlatformGrantService) Create(ctx context.Context, input PlatformGrantInput) (PlatformGrant, error) {
	input.PrincipalType = strings.TrimSpace(input.PrincipalType)
	input.PrincipalID = strings.TrimSpace(input.PrincipalID)
	input.PermissionID = strings.TrimSpace(input.PermissionID)
	if err := validatePlatformGrant(input); err != nil {
		return PlatformGrant{}, err
	}
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return PlatformGrant{}, err
	}
	id := uuid.NewString()
	now := time.Now()
	err = s.mutate(ctx, "authorization.platform-grant.create", id, func(tx *sqlx.Tx) error {
		if err := validatePrincipalExists(ctx, tx, input); err != nil {
			return err
		}
		var count int
		if err := tx.GetContext(ctx, &count, tx.Rebind(`SELECT count(*) FROM permissions p WHERE `+platformGrantablePermissionWhere+` AND p.id=?`), input.PermissionID); err != nil {
			return err
		}
		if count == 0 {
			return ErrPlatformGrantInvalid
		}
		_, err := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO platform_principal_permissions(id,principal_type,principal_id,permission_id,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,1)`), id, input.PrincipalType, input.PrincipalID, input.PermissionID, now, actor.ID, now, actor.ID)
		return err
	})
	if err != nil {
		return PlatformGrant{}, err
	}
	return s.Get(ctx, id)
}

func (s *PlatformGrantService) Get(ctx context.Context, id string) (PlatformGrant, error) {
	if _, err := uuid.Parse(id); err != nil {
		return PlatformGrant{}, ErrPlatformGrantInvalid
	}
	var row PlatformGrant
	err := s.db.GetContext(ctx, &row, s.db.Rebind(platformGrantSelect+` WHERE g.id=? AND g.deleted_at IS NULL AND p.deleted_at IS NULL`), id)
	if errors.Is(err, sql.ErrNoRows) {
		return PlatformGrant{}, ErrPlatformGrantNotFound
	}
	return row, err
}

func (s *PlatformGrantService) Page(ctx context.Context, input PlatformGrantPageInput) (PlatformGrantPage, error) {
	request, err := pagination.Normalize(input.Request)
	if err != nil || len(input.Keyword) > 256 || len(input.PermissionIDs) > 200 {
		return PlatformGrantPage{}, ErrPlatformGrantInvalid
	}
	where := `g.deleted_at IS NULL AND p.deleted_at IS NULL`
	args := []any{}
	if input.PrincipalType != "" {
		if input.PrincipalType != "user" && input.PrincipalType != "service_account" {
			return PlatformGrantPage{}, ErrPlatformGrantInvalid
		}
		where += ` AND g.principal_type=?`
		args = append(args, input.PrincipalType)
	}
	if input.PrincipalID != "" {
		if _, err := uuid.Parse(input.PrincipalID); err != nil {
			return PlatformGrantPage{}, ErrPlatformGrantInvalid
		}
		where += ` AND g.principal_id=?`
		args = append(args, input.PrincipalID)
	}
	if len(input.PermissionIDs) > 0 {
		for _, id := range input.PermissionIDs {
			if _, err := uuid.Parse(id); err != nil {
				return PlatformGrantPage{}, ErrPlatformGrantInvalid
			}
		}
		q, in, err := sqlx.In(`g.permission_id IN (?)`, input.PermissionIDs)
		if err != nil {
			return PlatformGrantPage{}, err
		}
		where += ` AND ` + q
		args = append(args, in...)
	}
	if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
		where += ` AND (LOWER(p.permission_key) LIKE LOWER(?) ESCAPE '!' OR LOWER(p.name) LIKE LOWER(?) ESCAPE '!')`
		like := database.LikeContainsPattern(keyword)
		args = append(args, like, like)
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM platform_principal_permissions g JOIN permissions p ON p.id=g.permission_id WHERE `+where), args...); err != nil {
		return PlatformGrantPage{}, err
	}
	rows := []PlatformGrant{}
	args = append(args, request.PageSize, pagination.Offset(request))
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(platformGrantSelect+` WHERE `+where+` ORDER BY g.created_at DESC,g.id DESC LIMIT ? OFFSET ?`), args...); err != nil {
		return PlatformGrantPage{}, err
	}
	return PlatformGrantPage{Items: rows, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *PlatformGrantService) Delete(ctx context.Context, id string, version int64) error {
	if _, err := uuid.Parse(id); err != nil || version <= 0 {
		return ErrPlatformGrantInvalid
	}
	return s.mutate(ctx, "authorization.platform-grant.delete", id, func(tx *sqlx.Tx) error {
		var current struct {
			Version int64 `db:"version"`
		}
		err := tx.GetContext(ctx, &current, tx.Rebind(`SELECT version FROM platform_principal_permissions WHERE id=? AND deleted_at IS NULL FOR UPDATE`), id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPlatformGrantNotFound
		}
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrPlatformGrantStaleVersion
		}
		res, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE platform_principal_permissions SET deleted_at=CURRENT_TIMESTAMP WHERE id=? AND version=? AND deleted_at IS NULL`), id, version)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrPlatformGrantStaleVersion
		}
		return nil
	})
}

func (s *PlatformGrantService) Update(ctx context.Context, id string, version int64, input PlatformGrantInput) (PlatformGrant, error) {
	input.PrincipalType = strings.TrimSpace(input.PrincipalType)
	input.PrincipalID = strings.TrimSpace(input.PrincipalID)
	input.PermissionID = strings.TrimSpace(input.PermissionID)
	if _, err := uuid.Parse(id); err != nil || version <= 0 || validatePlatformGrant(input) != nil {
		return PlatformGrant{}, ErrPlatformGrantInvalid
	}
	err := s.mutate(ctx, "authorization.platform-grant.update", id, func(tx *sqlx.Tx) error {
		if err := validatePrincipalExists(ctx, tx, input); err != nil {
			return err
		}
		var current struct {
			Version int64 `db:"version"`
		}
		err := tx.GetContext(ctx, &current, tx.Rebind(`SELECT version FROM platform_principal_permissions WHERE id=? AND deleted_at IS NULL FOR UPDATE`), id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPlatformGrantNotFound
		}
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrPlatformGrantStaleVersion
		}
		var count int
		if err := tx.GetContext(ctx, &count, tx.Rebind(`SELECT count(*) FROM permissions p WHERE `+platformGrantablePermissionWhere+` AND p.id=?`), input.PermissionID); err != nil {
			return err
		}
		if count == 0 {
			return ErrPlatformGrantInvalid
		}
		res, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE platform_principal_permissions SET principal_type=?,principal_id=?,permission_id=? WHERE id=? AND version=? AND deleted_at IS NULL`), input.PrincipalType, input.PrincipalID, input.PermissionID, id, version)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrPlatformGrantStaleVersion
		}
		return nil
	})
	if err != nil {
		return PlatformGrant{}, err
	}
	return s.Get(ctx, id)
}

func validatePrincipalExists(ctx context.Context, tx *sqlx.Tx, input PlatformGrantInput) error {
	query := `SELECT count(*) FROM identity_users WHERE id=? AND status='active' AND deleted_at IS NULL`
	if input.PrincipalType == "service_account" {
		query = `SELECT count(*) FROM identity_service_accounts WHERE id=? AND status='active' AND deleted_at IS NULL`
	}
	var count int
	if err := tx.GetContext(ctx, &count, tx.Rebind(query), input.PrincipalID); err != nil {
		return err
	}
	if count != 1 {
		return ErrPlatformGrantInvalid
	}
	return nil
}

func (s *PlatformGrantService) mutate(ctx context.Context, operation, id string, fn func(*sqlx.Tx) error) error {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return err
	}
	start := time.Now()
	log := operationlog.Entry{Operation: operation, ResourceType: "platform_grant", ResourceID: id, Source: "backend", Protocol: "service"}
	security := securitylog.Entry{EventType: securitylog.EventPermissionChanged, SubjectID: id, SubjectType: "platform_grant", Metadata: map[string]any{"operation": operation}}
	err := s.tx.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		log.Duration = time.Since(start)
		log.Succeeded = true
		security.Succeeded = true
		if s.operations != nil {
			if err := s.operations.RecordTx(ctx, tx, log); err != nil {
				return err
			}
		}
		if s.security != nil {
			return s.security.RecordTx(ctx, tx, security)
		}
		return nil
	})
	if database.IsUniqueViolation(err) {
		return ErrPlatformGrantConflict
	}
	if err != nil {
		log.Duration = time.Since(start)
		log.Succeeded = false
		log.ErrorCode = "operation_failed"
		log.ErrorMessage = "operation failed"
		security.Succeeded = false
		security.ErrorCode = "operation_failed"
		security.ErrorMessage = "operation failed"
		if s.operations != nil {
			_ = s.operations.Record(ctx, log)
		}
		if s.security != nil {
			_ = s.security.Record(ctx, security)
		}
		return err
	}
	return nil
}
