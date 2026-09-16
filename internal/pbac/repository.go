package pbac

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
)

const (
	policyColumns  = "id,code,name,description,scope,tenant_id,published_version_number,status,created_at,created_by,updated_at,updated_by,version"
	versionColumns = "id,policy_id,version_number,document,status,published_at,published_by,created_at,created_by,updated_at,updated_by,version"
)

// Repository persists PBAC policy identities and immutable versions.
type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository { return &Repository{db: db} }

// Get returns a policy visible to the active tenant. Global policies remain
// visible because their authorization is enforced independently by PBAC.
func (r *Repository) Get(ctx context.Context, id, tenantID string) (PolicyRecord, error) {
	if r == nil || r.db == nil {
		return PolicyRecord{}, errors.New("pbac policy database is disabled")
	}
	var record PolicyRecord
	query, args := visiblePolicyQuery("SELECT "+policyColumns+" FROM pbac_policies WHERE id=? AND deleted_at IS NULL", id, tenantID)
	if err := r.db.GetContext(ctx, &record, r.db.Rebind(query), args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PolicyRecord{}, ErrPolicyNotFound
		}
		return PolicyRecord{}, fmt.Errorf("get pbac policy: %w", err)
	}
	return record, nil
}

// GetVersion returns one immutable version through the same tenant boundary as
// its owning policy.
func (r *Repository) GetVersion(ctx context.Context, policyID string, versionNumber int64, tenantID string) (PolicyVersionRecord, error) {
	if r == nil || r.db == nil {
		return PolicyVersionRecord{}, errors.New("pbac policy database is disabled")
	}
	var record PolicyVersionRecord
	query := "SELECT " + prefixedVersionColumns("v") + " FROM pbac_policy_versions v JOIN pbac_policies p ON p.id=v.policy_id AND p.deleted_at IS NULL WHERE v.policy_id=? AND v.version_number=? AND v.deleted_at IS NULL"
	args := []any{policyID, versionNumber}
	if tenantID == "" {
		query += " AND p.scope='global'"
	} else {
		query += " AND (p.scope='global' OR (p.scope='tenant' AND p.tenant_id=?))"
		args = append(args, tenantID)
	}
	if err := r.db.GetContext(ctx, &record, r.db.Rebind(query), args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PolicyVersionRecord{}, ErrPolicyVersionMissing
		}
		return PolicyVersionRecord{}, fmt.Errorf("get pbac policy version: %w", err)
	}
	return record, nil
}

func (r *Repository) Page(ctx context.Context, tenantID string, input PolicyPageInput) (PolicyPage, error) {
	if r == nil || r.db == nil {
		return PolicyPage{}, errors.New("pbac policy database is disabled")
	}
	page, err := pagination.Normalize(input.Request)
	if err != nil {
		return PolicyPage{}, ErrInvalidPolicy
	}
	where, args := visiblePoliciesWhere(tenantID)
	if keyword := strings.ToLower(strings.TrimSpace(input.Keyword)); keyword != "" {
		if len(keyword) > 256 {
			return PolicyPage{}, ErrInvalidPolicy
		}
		where += " AND (lower(code) LIKE ? OR lower(name) LIKE ?)"
		like := "%" + keyword + "%"
		args = append(args, like, like)
	}
	where, args, err = appendPolicyFilter(where, args, "scope", policyScopeStrings(input.Scopes), []string{"global", "tenant"})
	if err != nil {
		return PolicyPage{}, err
	}
	where, args, err = appendPolicyFilter(where, args, "status", input.Statuses, []string{PolicyStatusActive, PolicyStatusDisabled})
	if err != nil {
		return PolicyPage{}, err
	}
	var total int64
	if err := r.db.GetContext(ctx, &total, r.db.Rebind("SELECT COUNT(*) FROM pbac_policies WHERE "+where), args...); err != nil {
		return PolicyPage{}, fmt.Errorf("count pbac policies: %w", err)
	}
	queryArgs := append(append([]any{}, args...), page.PageSize, pagination.Offset(page))
	var items []PolicyRecord
	if err := r.db.SelectContext(ctx, &items, r.db.Rebind("SELECT "+policyColumns+" FROM pbac_policies WHERE "+where+" ORDER BY updated_at DESC,id LIMIT ? OFFSET ?"), queryArgs...); err != nil {
		return PolicyPage{}, fmt.Errorf("page pbac policies: %w", err)
	}
	return PolicyPage{Items: items, Page: page.Page, PageSize: page.PageSize, Total: total}, nil
}

func (r *Repository) PageVersions(ctx context.Context, tenantID string, input PolicyVersionPageInput) (PolicyVersionPage, error) {
	if r == nil || r.db == nil {
		return PolicyVersionPage{}, errors.New("pbac policy database is disabled")
	}
	page, err := pagination.Normalize(input.Request)
	if err != nil || input.PolicyID == "" {
		return PolicyVersionPage{}, ErrInvalidPolicy
	}
	where := "v.policy_id=? AND v.deleted_at IS NULL AND p.deleted_at IS NULL"
	args := []any{input.PolicyID}
	if tenantID == "" {
		where += " AND p.scope='global'"
	} else {
		where += " AND (p.scope='global' OR (p.scope='tenant' AND p.tenant_id=?))"
		args = append(args, tenantID)
	}
	where, args, err = appendPolicyFilter(where, args, "v.status", input.Statuses, []string{VersionStatusDraft, VersionStatusPublished, VersionStatusArchived})
	if err != nil {
		return PolicyVersionPage{}, err
	}
	from := " FROM pbac_policy_versions v JOIN pbac_policies p ON p.id=v.policy_id WHERE " + where
	var total int64
	if err := r.db.GetContext(ctx, &total, r.db.Rebind("SELECT COUNT(*)"+from), args...); err != nil {
		return PolicyVersionPage{}, fmt.Errorf("count pbac policy versions: %w", err)
	}
	queryArgs := append(append([]any{}, args...), page.PageSize, pagination.Offset(page))
	var items []PolicyVersionRecord
	if err := r.db.SelectContext(ctx, &items, r.db.Rebind("SELECT "+prefixedVersionColumns("v")+from+" ORDER BY v.version_number DESC LIMIT ? OFFSET ?"), queryArgs...); err != nil {
		return PolicyVersionPage{}, fmt.Errorf("page pbac policy versions: %w", err)
	}
	return PolicyVersionPage{Items: items, Page: page.Page, PageSize: page.PageSize, Total: total}, nil
}

func visiblePoliciesWhere(tenantID string) (string, []any) {
	if tenantID == "" {
		return "deleted_at IS NULL AND scope='global'", nil
	}
	return "deleted_at IS NULL AND (scope='global' OR (scope='tenant' AND tenant_id=?))", []any{tenantID}
}

func appendPolicyFilter(where string, args []any, column string, values, allowed []string) (string, []any, error) {
	if len(values) == 0 {
		return where, args, nil
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}
	placeholders := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := allowedSet[value]; !ok {
			return "", nil, ErrInvalidPolicy
		}
		placeholders = append(placeholders, "?")
		args = append(args, value)
	}
	return where + " AND " + column + " IN (" + strings.Join(placeholders, ",") + ")", args, nil
}

func policyScopeStrings(scopes []PolicyScopeType) []string {
	values := make([]string, len(scopes))
	for i := range scopes {
		values[i] = string(scopes[i])
	}
	return values
}

// LoadAllPublished is an explicitly system-wide runtime operation. Normal
// tenant-facing reads must use Get or other tenant-scoped repository methods.
func (r *Repository) LoadAllPublished(ctx context.Context) ([]Policy, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("pbac policy database is disabled")
	}
	rows := []struct {
		Code     string          `db:"code"`
		Scope    PolicyScopeType `db:"scope"`
		TenantID *string         `db:"tenant_id"`
		Document string          `db:"document"`
	}{}
	query := `SELECT p.code,p.scope,p.tenant_id,v.document
		FROM pbac_policies p
		JOIN pbac_policy_versions v
		  ON v.policy_id=p.id AND v.version_number=p.published_version_number
		 AND v.status='published' AND v.deleted_at IS NULL
		WHERE p.status='active' AND p.deleted_at IS NULL
		ORDER BY p.scope,COALESCE(p.tenant_id,''),p.code`
	if err := r.db.SelectContext(ctx, &rows, query); err != nil {
		return nil, fmt.Errorf("load published pbac policies: %w", err)
	}
	policies := make([]Policy, 0, len(rows))
	for index, row := range rows {
		policy, err := ParsePolicy([]byte(row.Document))
		if err != nil {
			return nil, fmt.Errorf("parse published pbac policy %d: %w", index, err)
		}
		tenantID := ""
		if row.TenantID != nil {
			tenantID = *row.TenantID
		}
		if policy.Metadata.Code != row.Code || policy.Scope.Type != row.Scope || policy.Scope.TenantID != tenantID {
			return nil, fmt.Errorf("published pbac policy %d identity does not match its document", index)
		}
		policies = append(policies, policy)
	}
	return policies, nil
}

// Revision returns the authoritative database revision used by polling-based
// lost-notification recovery.
func (r *Repository) Revision(ctx context.Context) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("pbac policy database is disabled")
	}
	var revision struct {
		UpdatedAt  sql.NullTime `db:"updated_at"`
		RowCount   int64        `db:"row_count"`
		VersionSum int64        `db:"version_sum"`
	}
	query := `SELECT max(updated_at) AS updated_at,count(*) AS row_count,COALESCE(sum(version),0) AS version_sum FROM (
		SELECT updated_at,version FROM pbac_policies
		UNION ALL
		SELECT updated_at,version FROM pbac_policy_versions
		UNION ALL
		SELECT updated_at,version FROM pbac_policy_actions
	) pbac_revisions`
	if err := r.db.GetContext(ctx, &revision, query); err != nil {
		return "", fmt.Errorf("read pbac policy revision: %w", err)
	}
	return fmt.Sprintf("%s:%d:%d", revision.UpdatedAt.Time.UTC().Format(time.RFC3339Nano), revision.RowCount, revision.VersionSum), nil
}

func lockPolicy(ctx context.Context, tx *sqlx.Tx, id, tenantID string) (PolicyRecord, error) {
	var record PolicyRecord
	query, args := visiblePolicyQuery("SELECT "+policyColumns+" FROM pbac_policies WHERE id=? AND deleted_at IS NULL", id, tenantID)
	query += " FOR UPDATE"
	if err := tx.GetContext(ctx, &record, tx.Rebind(query), args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PolicyRecord{}, ErrPolicyNotFound
		}
		return PolicyRecord{}, fmt.Errorf("lock pbac policy: %w", err)
	}
	return record, nil
}

func getVersionTx(ctx context.Context, tx *sqlx.Tx, policyID string, versionNumber int64) (PolicyVersionRecord, error) {
	var record PolicyVersionRecord
	query := tx.Rebind("SELECT " + versionColumns + " FROM pbac_policy_versions WHERE policy_id=? AND version_number=? AND deleted_at IS NULL FOR UPDATE")
	if err := tx.GetContext(ctx, &record, query, policyID, versionNumber); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PolicyVersionRecord{}, ErrPolicyVersionMissing
		}
		return PolicyVersionRecord{}, fmt.Errorf("get pbac policy version: %w", err)
	}
	return record, nil
}

func visiblePolicyQuery(base, id, tenantID string) (string, []any) {
	if tenantID == "" {
		return base + " AND scope='global'", []any{id}
	}
	return base + " AND (scope='global' OR (scope='tenant' AND tenant_id=?))", []any{id, tenantID}
}

func prefixedVersionColumns(prefix string) string {
	return prefix + ".id," + prefix + ".policy_id," + prefix + ".version_number," +
		prefix + ".document," + prefix + ".status," + prefix + ".published_at," +
		prefix + ".published_by," + prefix + ".created_at," + prefix + ".created_by," +
		prefix + ".updated_at," + prefix + ".updated_by," + prefix + ".version"
}
