package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	appdb "github.com/lihongjie0209/go-api-template/internal/database"
)

var (
	ErrNotFound            = errors.New("tenant not found")
	ErrConflict            = errors.New("tenant conflict")
	ErrSecurityUnavailable = errors.New("tenant security audit unavailable")
)

type Repository struct{ db *sqlx.DB }

func NewRepository(db *sqlx.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Get(ctx context.Context, id, tenantID string) (Record, error) {
	if tenantID == "" {
		return Record{}, ErrForbidden
	}
	return r.get(ctx, `id = ? AND id = ? AND deleted_at IS NULL`, id, tenantID)
}

func (r *Repository) AdminGet(ctx context.Context, id string) (Record, error) {
	return r.get(ctx, `id = ? AND deleted_at IS NULL`, id)
}

func (r *Repository) get(ctx context.Context, where string, args ...any) (Record, error) {
	var record Record
	query := r.db.Rebind(`SELECT id, code, name, description, status, owner_user_id, owner_name, created_at, created_by, updated_at, updated_by, version FROM tenants WHERE ` + where)
	if err := r.db.GetContext(ctx, &record, query, args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, fmt.Errorf("get tenant: %w", err)
	}
	return record, nil
}

func (r *Repository) Page(ctx context.Context, tenantID string, input PageInput, limit, offset int) ([]Record, int64, error) {
	where := "deleted_at IS NULL"
	args := []any{}
	if tenantID != "" {
		where += " AND id = ?"
		args = append(args, tenantID)
	}
	if input.Keyword != "" {
		where += " AND (LOWER(code) LIKE ? OR LOWER(name) LIKE ?)"
		pattern := "%" + strings.ToLower(input.Keyword) + "%"
		args = append(args, pattern, pattern)
	}
	if len(input.IDs) > 0 {
		where += " AND id IN (?)"
		args = append(args, input.IDs)
	}
	if len(input.Statuses) > 0 {
		where += " AND status IN (?)"
		args = append(args, input.Statuses)
	}
	if input.CreatedAtFrom != nil {
		where += " AND created_at >= ?"
		args = append(args, *input.CreatedAtFrom)
	}
	if input.CreatedAtTo != nil {
		where += " AND created_at < ?"
		args = append(args, *input.CreatedAtTo)
	}
	var err error
	where, args, err = sqlx.In(where, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("build tenant filters: %w", err)
	}
	var total int64
	if err := r.db.GetContext(ctx, &total, r.db.Rebind("SELECT count(*) FROM tenants WHERE "+where), args...); err != nil {
		return nil, 0, fmt.Errorf("count tenants: %w", err)
	}
	queryArgs := append(append([]any{}, args...), limit, offset)
	records := []Record{}
	query := r.db.Rebind("SELECT id, code, name, description, status, owner_user_id, owner_name, created_at, created_by, updated_at, updated_by, version FROM tenants WHERE " + where + " ORDER BY created_at DESC, id LIMIT ? OFFSET ?")
	if err := r.db.SelectContext(ctx, &records, query, queryArgs...); err != nil {
		return nil, 0, fmt.Errorf("page tenants: %w", err)
	}
	return records, total, nil
}

func insertTenant(ctx context.Context, tx *sqlx.Tx, record Record, actorID string) error {
	now := time.Now()
	query := tx.Rebind(`INSERT INTO tenants (id, code, name, description, status, owner_user_id, owner_name, created_at, created_by, updated_at, updated_by, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if _, err := tx.ExecContext(ctx, query, record.ID, record.Code, record.Name, record.Description, record.Status, record.OwnerUserID, record.OwnerName, now, actorID, now, actorID, 1); err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return fmt.Errorf("insert tenant: %w", err)
	}
	return nil
}
func insertOwnerMembership(ctx context.Context, tx *sqlx.Tx, id string, record Record, username, actorID string) error {
	now := time.Now()
	query := tx.Rebind(`INSERT INTO tenant_memberships (id, tenant_id, user_id, username, display_name, status, joined_at, created_at, created_by, updated_at, updated_by, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if _, err := tx.ExecContext(ctx, query, id, record.ID, record.OwnerUserID, username, record.OwnerName, StatusActive, now, now, actorID, now, actorID, 1); err != nil {
		return fmt.Errorf("insert owner membership: %w", err)
	}
	return nil
}
func insertTenantAdministrator(ctx context.Context, tx *sqlx.Tx, id, tenantID, membershipID, actorID string) error {
	now := time.Now()
	query := tx.Rebind(`INSERT INTO tenant_administrators (id, tenant_id, membership_id, created_at, created_by, updated_at, updated_by, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if _, err := tx.ExecContext(ctx, query, id, tenantID, membershipID, now, actorID, now, actorID, 1); err != nil {
		return fmt.Errorf("insert tenant administrator: %w", err)
	}
	return nil
}
func updateTenant(ctx context.Context, tx *sqlx.Tx, id, name, description string, status Status, version int64, actorID string) error {
	now := time.Now()
	query := tx.Rebind(`UPDATE tenants SET name = ?, description = ?, status = ?, updated_at = ?, updated_by = ?, version = version + 1 WHERE id = ? AND version = ? AND deleted_at IS NULL`)
	result, err := tx.ExecContext(ctx, query, name, description, status, now, actorID, id, version)
	if err != nil {
		return fmt.Errorf("update tenant: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrConflict
	}
	return nil
}
func deleteTenant(ctx context.Context, tx *sqlx.Tx, id string, version int64, actorID string) error {
	now := time.Now()
	for _, table := range []string{"tenant_member_roles", "tenant_department_members", "tenant_role_permissions", "tenant_roles", "tenant_administrators", "tenant_permission_grants", "tenant_application_grants", "tenant_memberships", "tenant_departments"} {
		query := tx.Rebind(`UPDATE ` + table + ` SET deleted_at = ?, deleted_by = ?, updated_at = ?, updated_by = ?, version = version + 1 WHERE tenant_id = ? AND deleted_at IS NULL`)
		if _, err := tx.ExecContext(ctx, query, now, actorID, now, actorID, id); err != nil {
			return fmt.Errorf("delete tenant dependents from %s: %w", table, err)
		}
	}
	query := tx.Rebind(`UPDATE tenants SET deleted_at = ?, deleted_by = ?, updated_at = ?, updated_by = ?, version = version + 1 WHERE id = ? AND version = ? AND deleted_at IS NULL`)
	result, err := tx.ExecContext(ctx, query, now, actorID, now, actorID, id, version)
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrConflict
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return appdb.IsUniqueViolation(err)
}
