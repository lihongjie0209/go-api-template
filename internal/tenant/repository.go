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
	ErrStaleVersion        = errors.New("tenant stale version")
	ErrSecurityUnavailable = errors.New("tenant security audit unavailable")
)

type Repository struct{ db *sqlx.DB }

func NewRepository(db *sqlx.DB) *Repository { return &Repository{db: db} }

func (r *Repository) DeleteTarget(ctx context.Context, tenantID string) (DeleteTarget, error) {
	if strings.TrimSpace(tenantID) == "" {
		return DeleteTarget{}, ErrForbidden
	}
	return r.deleteTarget(ctx, tenantID)
}

func (r *Repository) UpdateTarget(ctx context.Context, tenantID string) (UpdateTarget, error) {
	if strings.TrimSpace(tenantID) == "" {
		return UpdateTarget{}, ErrForbidden
	}
	return r.updateTarget(ctx, tenantID)
}

// AdminUpdateTarget is called only after the platform principal is checked.
func (r *Repository) AdminUpdateTarget(ctx context.Context, id string) (UpdateTarget, error) {
	if strings.TrimSpace(id) == "" {
		return UpdateTarget{}, ErrInvalid
	}
	return r.updateTarget(ctx, id)
}

func (r *Repository) updateTarget(ctx context.Context, id string) (UpdateTarget, error) {
	var target UpdateTarget
	query := r.db.Rebind(`SELECT id, code, name, description, status, version FROM tenants WHERE id = ? AND deleted_at IS NULL`)
	if err := r.db.GetContext(ctx, &target, query, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UpdateTarget{}, ErrNotFound
		}
		return UpdateTarget{}, fmt.Errorf("get tenant update target: %w", err)
	}
	return target, nil
}

// AdminDeleteTarget is called only after Service.AdminDeleteTarget establishes
// a platform principal; it must not be used for tenant-scoped traffic.
func (r *Repository) AdminDeleteTarget(ctx context.Context, id string) (DeleteTarget, error) {
	if strings.TrimSpace(id) == "" {
		return DeleteTarget{}, ErrInvalid
	}
	return r.deleteTarget(ctx, id)
}

func (r *Repository) deleteTarget(ctx context.Context, id string) (DeleteTarget, error) {
	var target DeleteTarget
	query := r.db.Rebind(`SELECT id, code, name, status, version FROM tenants WHERE id = ? AND deleted_at IS NULL`)
	if err := r.db.GetContext(ctx, &target, query, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeleteTarget{}, ErrNotFound
		}
		return DeleteTarget{}, fmt.Errorf("get tenant delete target: %w", err)
	}
	return target, nil
}

func (r *Repository) Get(ctx context.Context, id, tenantID string) (Record, error) {
	if tenantID == "" {
		return Record{}, ErrForbidden
	}
	return r.get(ctx, `id = ? AND id = ? AND deleted_at IS NULL`, id, tenantID)
}

func (r *Repository) AdminGet(ctx context.Context, id string) (Record, error) {
	return r.get(ctx, `id = ? AND deleted_at IS NULL`, id)
}

// getTx captures the committed mutation response before the transaction ends.
// A failed read must roll back the write rather than fail after commit.
func (r *Repository) getTx(ctx context.Context, tx *sqlx.Tx, id string) (Record, error) {
	return r.getTxWhere(ctx, tx, `id = ? AND deleted_at IS NULL`, id)
}

func (r *Repository) getTenantTx(ctx context.Context, tx *sqlx.Tx, id, tenantID string) (Record, error) {
	if strings.TrimSpace(tenantID) == "" {
		return Record{}, ErrForbidden
	}
	return r.getTxWhere(ctx, tx, `id = ? AND id = ? AND deleted_at IS NULL`, id, tenantID)
}

func (r *Repository) getTxWhere(ctx context.Context, tx *sqlx.Tx, where string, args ...any) (Record, error) {
	var record Record
	query := tx.Rebind(`SELECT id, code, name, description, status, owner_user_id, owner_name, created_at, created_by, updated_at, updated_by, version FROM tenants WHERE ` + where)
	if err := tx.GetContext(ctx, &record, query, args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, fmt.Errorf("get tenant in transaction: %w", err)
	}
	return record, nil
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
	if strings.TrimSpace(tenantID) == "" {
		return nil, 0, ErrForbidden
	}
	return r.page(ctx, "deleted_at IS NULL AND id = ?", []any{tenantID}, input, limit, offset)
}

// AdminPage is called only after Service.AdminPage checks the platform actor.
// Keeping unscoped access separately named prevents an empty tenant ID from
// silently widening an ordinary repository query.
func (r *Repository) AdminPage(ctx context.Context, input PageInput, limit, offset int) ([]Record, int64, error) {
	return r.page(ctx, "deleted_at IS NULL", nil, input, limit, offset)
}

func (r *Repository) page(ctx context.Context, where string, args []any, input PageInput, limit, offset int) ([]Record, int64, error) {
	if input.Keyword != "" {
		where += " AND (LOWER(code) LIKE ? ESCAPE '!' OR LOWER(name) LIKE ? ESCAPE '!')"
		pattern := appdb.LikeContainsPattern(strings.ToLower(input.Keyword))
		args = append(args, pattern, pattern)
	}
	if len(input.IDs) > 0 {
		where += " AND id IN (?)"
		args = append(args, input.IDs)
	}
	if len(input.Codes) > 0 {
		where += " AND code IN (?)"
		args = append(args, input.Codes)
	}
	if len(input.OwnerUserIDs) > 0 {
		where += " AND owner_user_id IN (?)"
		args = append(args, input.OwnerUserIDs)
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
		return ErrStaleVersion
	}
	return nil
}
func deleteTenant(ctx context.Context, tx *sqlx.Tx, id string, version int64, actorID string) error {
	now := time.Now()
	policyCleanup := []struct {
		table string
		query string
	}{
		{
			table: "data_permission_policy_actions",
			query: `UPDATE data_permission_policy_actions SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE deleted_at IS NULL AND policy_version_id IN (
				SELECT v.id FROM data_permission_policy_versions v JOIN data_permission_policies p ON p.id=v.policy_id WHERE p.scope='tenant' AND p.tenant_id=?)`,
		},
		{
			table: "data_permission_policy_versions",
			query: `UPDATE data_permission_policy_versions SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE deleted_at IS NULL AND policy_id IN (
				SELECT p.id FROM data_permission_policies p WHERE p.scope='tenant' AND p.tenant_id=?)`,
		},
		{
			table: "data_permission_policies",
			query: `UPDATE data_permission_policies SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE deleted_at IS NULL AND scope='tenant' AND tenant_id=?`,
		},
	}
	for _, cleanup := range policyCleanup {
		if _, err := tx.ExecContext(ctx, tx.Rebind(cleanup.query), now, actorID, now, actorID, id); err != nil {
			return fmt.Errorf("delete tenant dependents from %s: %w", cleanup.table, err)
		}
	}
	for _, table := range []string{"application_session_contexts", "tenant_member_roles", "tenant_department_members", "tenant_role_permissions", "tenant_roles", "tenant_administrators", "tenant_permission_grants", "tenant_application_grants", "tenant_memberships", "tenant_departments"} {
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
		return ErrStaleVersion
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return appdb.IsUniqueViolation(err)
}
