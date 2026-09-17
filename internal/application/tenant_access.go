package application

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
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

const grantColumns = `g.id,g.tenant_id,t.name tenant_name,g.application_id,a.code application_code,a.name application_name,a.icon application_icon,a.home_path application_home_path,g.status,g.starts_at,g.expires_at,g.created_at,g.created_by,g.updated_at,g.updated_by,g.version`

var (
	ErrGrantInvalid   = errors.New("invalid tenant application grant")
	ErrGrantNotFound  = errors.New("tenant application grant not found")
	ErrGrantConflict  = errors.New("tenant application grant conflict")
	ErrGrantForbidden = errors.New("tenant application grant forbidden")
)

type Grant struct {
	ID                  string     `db:"id" json:"id"`
	TenantID            string     `db:"tenant_id" json:"tenant_id"`
	TenantName          string     `db:"tenant_name" json:"tenant_name"`
	ApplicationID       string     `db:"application_id" json:"application_id"`
	ApplicationCode     string     `db:"application_code" json:"application_code"`
	ApplicationName     string     `db:"application_name" json:"application_name"`
	ApplicationIcon     string     `db:"application_icon" json:"application_icon"`
	ApplicationHomePath string     `db:"application_home_path" json:"application_home_path"`
	Status              string     `db:"status" json:"status"`
	StartsAt            *time.Time `db:"starts_at" json:"starts_at,omitempty"`
	ExpiresAt           *time.Time `db:"expires_at" json:"expires_at,omitempty"`
	CreatedAt           time.Time  `db:"created_at" json:"created_at"`
	CreatedBy           string     `db:"created_by" json:"created_by"`
	CreatedByName       string     `db:"-" json:"created_by_name"`
	UpdatedAt           time.Time  `db:"updated_at" json:"updated_at"`
	UpdatedBy           string     `db:"updated_by" json:"updated_by"`
	UpdatedByName       string     `db:"-" json:"updated_by_name"`
	Version             int64      `db:"version" json:"version"`
}

type Current struct {
	ID        string     `db:"id" json:"id"`
	Code      string     `db:"code" json:"code"`
	Name      string     `db:"name" json:"name"`
	Icon      string     `db:"icon" json:"icon"`
	HomePath  string     `db:"home_path" json:"home_path"`
	SortOrder int64      `db:"sort_order" json:"sort_order"`
	StartsAt  *time.Time `db:"starts_at" json:"starts_at,omitempty"`
	ExpiresAt *time.Time `db:"expires_at" json:"expires_at,omitempty"`
}

type GrantInput struct {
	TenantID, ApplicationID string
	StartsAt, ExpiresAt     *time.Time
	Version                 int64
}

type GrantPageInput struct {
	pagination.Request
	TenantID                   string
	ApplicationIDs, Statuses   []string
	CreatedAtFrom, CreatedAtTo *time.Time
	Sort                       []pagination.Sort
}

type GrantPage = pagination.Result[Grant]

type TenantAccessService struct {
	db         *sqlx.DB
	tx         *database.Transactor
	operations operationlog.TransactionalRecorder
	security   securitylog.TransactionalRecorder
	actors     presentation.ActorResolver
}

func NewTenantAccessService(db *sqlx.DB, tx *database.Transactor, operations operationlog.TransactionalRecorder, security securitylog.TransactionalRecorder, actors presentation.ActorResolver) *TenantAccessService {
	return &TenantAccessService{db: db, tx: tx, operations: operations, security: security, actors: actors}
}

func (s *TenantAccessService) Grant(ctx context.Context, input GrantInput) (Grant, error) {
	actor, err := platformActor(ctx)
	if err != nil {
		return Grant{}, err
	}
	input.TenantID, input.ApplicationID = strings.TrimSpace(input.TenantID), strings.TrimSpace(input.ApplicationID)
	if !validGrantInput(input) {
		return Grant{}, ErrGrantInvalid
	}
	id := uuid.NewString()
	err = s.mutate(ctx, "tenant.application.grant", input.TenantID, input.ApplicationID, input, func(tx *sqlx.Tx) (string, string, error) {
		var tenantName, applicationName string
		if err := tx.GetContext(ctx, &tenantName, tx.Rebind(`SELECT name FROM tenants WHERE id=? AND deleted_at IS NULL FOR UPDATE`), input.TenantID); err != nil {
			return "", "", mapGrantReferenceError(err)
		}
		if err := tx.GetContext(ctx, &applicationName, tx.Rebind(`SELECT name FROM applications WHERE id=? AND deleted_at IS NULL FOR UPDATE`), input.ApplicationID); err != nil {
			return "", "", mapGrantReferenceError(err)
		}
		now := time.Now()
		if input.Version == 0 {
			_, err := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO tenant_application_grants(id,tenant_id,application_id,status,starts_at,expires_at,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,1)`), id, input.TenantID, input.ApplicationID, "active", input.StartsAt, input.ExpiresAt, now, actor.ID, now, actor.ID)
			return tenantName, applicationName, err
		}
		var existingID string
		if err := tx.GetContext(ctx, &existingID, tx.Rebind(`SELECT id FROM tenant_application_grants WHERE tenant_id=? AND application_id=? AND deleted_at IS NULL FOR UPDATE`), input.TenantID, input.ApplicationID); err != nil {
			return "", "", mapGrantReferenceError(err)
		}
		id = existingID
		result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_application_grants SET status='active',starts_at=?,expires_at=?,updated_at=?,updated_by=? WHERE tenant_id=? AND application_id=? AND version=? AND deleted_at IS NULL`), input.StartsAt, input.ExpiresAt, now, actor.ID, input.TenantID, input.ApplicationID, input.Version)
		if err != nil {
			return "", "", err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return "", "", err
		}
		if affected != 1 {
			return "", "", ErrGrantConflict
		}
		return tenantName, applicationName, nil
	})
	if database.IsUniqueViolation(err) {
		return Grant{}, ErrGrantConflict
	}
	if err != nil {
		return Grant{}, err
	}
	return s.AdminGet(ctx, input.TenantID, id)
}

func (s *TenantAccessService) Revoke(ctx context.Context, tenantID, id string, version int64) error {
	actor, err := platformActor(ctx)
	if err != nil {
		return err
	}
	tenantID, id = strings.TrimSpace(tenantID), strings.TrimSpace(id)
	if tenantID == "" || id == "" || len(tenantID) > 128 || len(id) > 128 || version <= 0 {
		return ErrGrantInvalid
	}
	return s.mutate(ctx, "tenant.application.revoke", tenantID, id, map[string]any{"id": id, "version": version}, func(tx *sqlx.Tx) (string, string, error) {
		var names struct{ Tenant, Application string }
		query := `SELECT t.name tenant,a.name application FROM tenant_application_grants g JOIN tenants t ON t.id=g.tenant_id AND t.deleted_at IS NULL JOIN applications a ON a.id=g.application_id AND a.deleted_at IS NULL WHERE g.tenant_id=? AND g.id=? AND g.deleted_at IS NULL FOR UPDATE`
		if err := tx.GetContext(ctx, &names, tx.Rebind(query), tenantID, id); errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrGrantNotFound
		} else if err != nil {
			return "", "", err
		}
		result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_application_grants SET status='revoked',updated_at=?,updated_by=? WHERE tenant_id=? AND id=? AND version=? AND status='active' AND deleted_at IS NULL`), time.Now(), actor.ID, tenantID, id, version)
		if err != nil {
			return "", "", err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return "", "", err
		}
		if affected != 1 {
			return "", "", ErrGrantConflict
		}
		return names.Tenant, names.Application, nil
	})
}

func (s *TenantAccessService) AdminGet(ctx context.Context, tenantID, id string) (Grant, error) {
	if _, err := platformActor(ctx); err != nil {
		return Grant{}, err
	}
	tenantID, id = strings.TrimSpace(tenantID), strings.TrimSpace(id)
	if tenantID == "" || id == "" || len(tenantID) > 128 || len(id) > 128 {
		return Grant{}, ErrGrantInvalid
	}
	var result Grant
	query := `SELECT ` + grantColumns + ` FROM tenant_application_grants g JOIN tenants t ON t.id=g.tenant_id AND t.deleted_at IS NULL JOIN applications a ON a.id=g.application_id AND a.deleted_at IS NULL WHERE g.tenant_id=? AND g.id=? AND g.deleted_at IS NULL`
	if err := s.db.GetContext(ctx, &result, s.db.Rebind(query), tenantID, id); errors.Is(err, sql.ErrNoRows) {
		return Grant{}, ErrGrantNotFound
	} else if err != nil {
		return Grant{}, err
	}
	items := []Grant{result}
	if err := s.present(ctx, items); err != nil {
		return Grant{}, err
	}
	return items[0], nil
}

func (s *TenantAccessService) Page(ctx context.Context, input GrantPageInput) (GrantPage, error) {
	if _, err := platformActor(ctx); err != nil {
		return GrantPage{}, err
	}
	input.TenantID = strings.TrimSpace(input.TenantID)
	request, err := pagination.Normalize(input.Request)
	if err != nil || input.TenantID == "" || len(input.TenantID) > 128 || len(input.ApplicationIDs) > 200 || len(input.Statuses) > 2 || len(input.Sort) > 3 || len(strings.TrimSpace(request.Keyword)) > 256 || (input.CreatedAtFrom != nil && input.CreatedAtTo != nil && !input.CreatedAtFrom.Before(*input.CreatedAtTo)) {
		return GrantPage{}, ErrGrantInvalid
	}
	where := []string{"g.tenant_id=?", "g.deleted_at IS NULL", "t.deleted_at IS NULL", "a.deleted_at IS NULL"}
	args := []any{input.TenantID}
	if keyword := strings.TrimSpace(request.Keyword); keyword != "" {
		where = append(where, `(LOWER(a.code) LIKE LOWER(?) OR LOWER(a.name) LIKE LOWER(?))`)
		value := "%" + keyword + "%"
		args = append(args, value, value)
	}
	var buildErr error
	where, args, buildErr = appendIn(where, args, "g.application_id", input.ApplicationIDs)
	if buildErr == nil {
		where, args, buildErr = appendIn(where, args, "g.status", input.Statuses)
	}
	if buildErr != nil {
		return GrantPage{}, ErrGrantInvalid
	}
	for _, status := range input.Statuses {
		if status != "active" && status != "revoked" {
			return GrantPage{}, ErrGrantInvalid
		}
	}
	for _, applicationID := range input.ApplicationIDs {
		if strings.TrimSpace(applicationID) == "" || len(applicationID) > 128 {
			return GrantPage{}, ErrGrantInvalid
		}
	}
	if input.CreatedAtFrom != nil {
		where, args = append(where, "g.created_at>=?"), append(args, *input.CreatedAtFrom)
	}
	if input.CreatedAtTo != nil {
		where, args = append(where, "g.created_at<?"), append(args, *input.CreatedAtTo)
	}
	order, err := grantOrder(input.Sort)
	if err != nil {
		return GrantPage{}, err
	}
	from := ` FROM tenant_application_grants g JOIN tenants t ON t.id=g.tenant_id JOIN applications a ON a.id=g.application_id WHERE ` + strings.Join(where, " AND ")
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*)`+from), args...); err != nil {
		return GrantPage{}, err
	}
	items := []Grant{}
	pageArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(`SELECT `+grantColumns+from+` ORDER BY `+order+` LIMIT ? OFFSET ?`), pageArgs...); err != nil {
		return GrantPage{}, err
	}
	if err := s.present(ctx, items); err != nil {
		return GrantPage{}, err
	}
	return GrantPage{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func grantOrder(sorts []pagination.Sort) (string, error) {
	if len(sorts) == 0 {
		return "g.created_at DESC,g.id ASC", nil
	}
	allowed := map[string]string{"application_code": "a.code", "application_name": "a.name", "status": "g.status", "starts_at": "g.starts_at", "expires_at": "g.expires_at", "created_at": "g.created_at", "updated_at": "g.updated_at"}
	parts, seen := make([]string, 0, len(sorts)+1), make(map[string]struct{}, len(sorts))
	for _, sort := range sorts {
		column, ok := allowed[sort.Field]
		direction := strings.ToUpper(sort.Direction)
		if !ok || (direction != "ASC" && direction != "DESC") {
			return "", ErrGrantInvalid
		}
		if _, duplicate := seen[column]; duplicate {
			return "", ErrGrantInvalid
		}
		seen[column] = struct{}{}
		parts = append(parts, column+" "+direction)
	}
	return strings.Join(append(parts, "g.id ASC"), ","), nil
}

func (s *TenantAccessService) Current(ctx context.Context) ([]Current, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.Type != platformprincipal.TypeUser || actor.TenantID == "" || actor.MembershipID == "" {
		return nil, ErrGrantForbidden
	}
	items := []Current{}
	query := `SELECT a.id,a.code,a.name,a.icon,a.home_path,a.sort_order,g.starts_at,g.expires_at FROM tenant_application_grants g JOIN applications a ON a.id=g.application_id AND a.status='active' AND a.deleted_at IS NULL JOIN tenant_memberships m ON m.tenant_id=g.tenant_id AND m.id=? AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL WHERE g.tenant_id=? AND g.status='active' AND g.deleted_at IS NULL AND (g.starts_at IS NULL OR g.starts_at<=?) AND (g.expires_at IS NULL OR g.expires_at>?) ORDER BY a.sort_order,a.id`
	now := time.Now()
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(query), actor.MembershipID, actor.ID, actor.TenantID, now, now); err != nil {
		return nil, err
	}
	for index := range items {
		if items[index].StartsAt != nil {
			value := presentation.Time(*items[index].StartsAt)
			items[index].StartsAt = &value
		}
		if items[index].ExpiresAt != nil {
			value := presentation.Time(*items[index].ExpiresAt)
			items[index].ExpiresAt = &value
		}
	}
	return items, nil
}

func platformActor(ctx context.Context) (platformprincipal.Principal, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.TenantID != "" {
		return platformprincipal.Principal{}, ErrGrantForbidden
	}
	return actor, nil
}

func validGrantInput(input GrantInput) bool {
	return input.TenantID != "" && input.ApplicationID != "" && len(input.TenantID) <= 128 && len(input.ApplicationID) <= 128 && input.Version >= 0 && (input.StartsAt == nil || input.ExpiresAt == nil || input.StartsAt.Before(*input.ExpiresAt))
}

func mapGrantReferenceError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGrantNotFound
	}
	return err
}

func (s *TenantAccessService) present(ctx context.Context, items []Grant) error {
	ids := make([]string, 0, len(items)*2)
	for _, item := range items {
		ids = append(ids, item.CreatedBy, item.UpdatedBy)
	}
	names, err := presentation.ActorNames(ctx, s.actors, ids...)
	if err != nil {
		return err
	}
	for index := range items {
		items[index].CreatedByName, items[index].UpdatedByName = names[items[index].CreatedBy], names[items[index].UpdatedBy]
		items[index].CreatedAt, items[index].UpdatedAt = presentation.Time(items[index].CreatedAt), presentation.Time(items[index].UpdatedAt)
		if items[index].StartsAt != nil {
			value := presentation.Time(*items[index].StartsAt)
			items[index].StartsAt = &value
		}
		if items[index].ExpiresAt != nil {
			value := presentation.Time(*items[index].ExpiresAt)
			items[index].ExpiresAt = &value
		}
	}
	return nil
}

func (s *TenantAccessService) mutate(ctx context.Context, operation, tenantID, subjectID string, request any, fn func(*sqlx.Tx) (string, string, error)) error {
	started := time.Now()
	operationEntry := operationlog.Entry{Operation: operation, ResourceType: "tenant_application_grant", ResourceID: subjectID, Source: "backend", Protocol: "service", Request: request}
	securityEntry := securitylog.Entry{EventType: securitylog.EventTenantAuthorization, SubjectID: subjectID, SubjectType: "application", TenantID: tenantID, Metadata: map[string]any{"operation": operation}}
	err := s.tx.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
		tenantName, applicationName, err := fn(tx)
		if err != nil {
			return err
		}
		operationEntry.ResourceName = applicationName
		operationEntry.Duration, operationEntry.Succeeded = time.Since(started), true
		securityEntry.SubjectName, securityEntry.Succeeded = applicationName, true
		securityEntry.Metadata = map[string]any{"operation": operation, "tenant_name": tenantName}
		if s.operations != nil {
			if err := s.operations.RecordTx(ctx, tx, operationEntry); err != nil {
				return err
			}
		}
		if s.security != nil {
			return s.security.RecordTx(ctx, tx, securityEntry)
		}
		return nil
	})
	if err == nil {
		return nil
	}
	operationEntry.Duration, operationEntry.Succeeded, operationEntry.ErrorCode, operationEntry.ErrorMessage = time.Since(started), false, "operation_failed", "operation failed"
	securityEntry.Succeeded, securityEntry.ErrorCode, securityEntry.ErrorMessage = false, "tenant_application_authorization_failed", "tenant application authorization failed"
	if s.operations != nil {
		_ = s.operations.Record(ctx, operationEntry)
	}
	if s.security != nil {
		if recordErr := s.security.Record(ctx, securityEntry); recordErr != nil && s.security.FailClosed() {
			return fmt.Errorf("%w: %v", err, recordErr)
		}
	}
	return err
}
