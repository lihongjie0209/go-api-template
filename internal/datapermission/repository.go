package datapermission

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/pagination"
)

// Revision returns the authoritative database revision used to recover from
// lost Redis invalidation notifications.
func (r *Repository) Revision(ctx context.Context) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("data permission: policy database is disabled")
	}
	var revision struct {
		UpdatedAt  sql.NullTime `db:"updated_at"`
		RowCount   int64        `db:"row_count"`
		VersionSum int64        `db:"version_sum"`
	}
	query := `SELECT max(updated_at) AS updated_at,count(*) AS row_count,COALESCE(sum(version),0) AS version_sum FROM (
		SELECT updated_at,version FROM data_permission_policies
		UNION ALL SELECT updated_at,version FROM data_permission_policy_versions
		UNION ALL SELECT updated_at,version FROM data_permission_policy_actions
	) data_permission_revisions`
	if err := r.db.GetContext(ctx, &revision, query); err != nil {
		return "", fmt.Errorf("read data permission policy revision: %w", err)
	}
	return fmt.Sprintf("%s:%d:%d", revision.UpdatedAt.Time.UTC().Format(time.RFC3339Nano), revision.RowCount, revision.VersionSum), nil
}

const (
	policyColumns  = "id,code,name,scope,tenant_id,published_version_number,status,created_at,created_by,updated_at,updated_by,version"
	versionColumns = "id,policy_id,version_number,document,status,published_at,published_by,created_at,created_by,updated_at,updated_by,version"
)

func (r *Repository) Get(ctx context.Context, id, tenantID string) (PolicyRecord, error) {
	if r == nil || r.db == nil {
		return PolicyRecord{}, errors.New("data permission: policy database is disabled")
	}
	query, args := visiblePolicyQuery("SELECT "+policyColumns+" FROM data_permission_policies WHERE id=? AND deleted_at IS NULL", []any{id}, tenantID)
	var record PolicyRecord
	if err := r.db.GetContext(ctx, &record, r.db.Rebind(query), args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return record, ErrPolicyNotFound
		}
		return record, fmt.Errorf("get data permission policy: %w", err)
	}
	return record, nil
}

func (r *Repository) GetVersion(ctx context.Context, policyID string, versionNumber int64, tenantID string) (VersionRecord, error) {
	if r == nil || r.db == nil {
		return VersionRecord{}, errors.New("data permission: policy database is disabled")
	}
	query := "SELECT " + prefixedVersionColumns("v") + " FROM data_permission_policy_versions v JOIN data_permission_policies p ON p.id=v.policy_id AND p.deleted_at IS NULL WHERE v.policy_id=? AND v.version_number=? AND v.deleted_at IS NULL"
	args := []any{policyID, versionNumber}
	query, args = visiblePolicyWhere(query, args, tenantID)
	var record VersionRecord
	if err := r.db.GetContext(ctx, &record, r.db.Rebind(query), args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return record, ErrPolicyNotFound
		}
		return record, fmt.Errorf("get data permission policy version: %w", err)
	}
	return record, nil
}

func (r *Repository) Page(ctx context.Context, tenantID string, input PolicyPageInput) (PolicyPage, error) {
	page, err := pagination.Normalize(input.Request)
	if err != nil || !validPolicyPageFilters(input.IDs, input.Codes, input.CreatedAtFrom, input.CreatedAtTo, input.UpdatedAtFrom, input.UpdatedAtTo) || len(input.Scopes) > 2 || len(input.Statuses) > 2 {
		return PolicyPage{}, ErrInvalidPolicy
	}
	where, args := visiblePoliciesWhere(tenantID)
	if keyword := strings.ToLower(strings.TrimSpace(input.Keyword)); keyword != "" {
		if len(keyword) > 256 {
			return PolicyPage{}, ErrInvalidPolicy
		}
		where += " AND (lower(code) LIKE ? OR lower(name) LIKE ?)"
		args = append(args, "%"+keyword+"%", "%"+keyword+"%")
	}
	where, args, err = appendEnumFilter(where, args, "scope", scopeValues(input.Scopes), []string{string(PolicyScopeGlobal), string(PolicyScopeTenant)})
	if err != nil {
		return PolicyPage{}, err
	}
	where, args, err = appendEnumFilter(where, args, "status", input.Statuses, []string{StatusActive, StatusDisabled})
	if err != nil {
		return PolicyPage{}, err
	}
	where, args = appendStringSetFilter(where, args, "id", input.IDs)
	where, args = appendStringSetFilter(where, args, "code", input.Codes)
	where, args = appendTimeRange(where, args, "created_at", input.CreatedAtFrom, input.CreatedAtTo)
	where, args = appendTimeRange(where, args, "updated_at", input.UpdatedAtFrom, input.UpdatedAtTo)
	var total int64
	if err := r.db.GetContext(ctx, &total, r.db.Rebind("SELECT count(*) FROM data_permission_policies WHERE "+where), args...); err != nil {
		return PolicyPage{}, fmt.Errorf("count data permission policies: %w", err)
	}
	queryArgs := append(append([]any{}, args...), page.PageSize, pagination.Offset(page))
	items := []PolicyRecord{}
	if err := r.db.SelectContext(ctx, &items, r.db.Rebind("SELECT "+policyColumns+" FROM data_permission_policies WHERE "+where+" ORDER BY updated_at DESC,id LIMIT ? OFFSET ?"), queryArgs...); err != nil {
		return PolicyPage{}, fmt.Errorf("page data permission policies: %w", err)
	}
	return PolicyPage{Items: items, Page: page.Page, PageSize: page.PageSize, Total: total}, nil
}

func validPolicyPageFilters(ids, codes []string, createdFrom, createdTo, updatedFrom, updatedTo *time.Time) bool {
	if len(ids) > 200 || len(codes) > 200 || createdFrom != nil && createdTo != nil && !createdFrom.Before(*createdTo) || updatedFrom != nil && updatedTo != nil && !updatedFrom.Before(*updatedTo) {
		return false
	}
	for _, value := range ids {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
			return false
		}
	}
	for _, value := range codes {
		if !policyCode.MatchString(value) {
			return false
		}
	}
	return true
}

func appendStringSetFilter(where string, args []any, column string, values []string) (string, []any) {
	if len(values) == 0 {
		return where, args
	}
	placeholders := make([]string, len(values))
	for index, value := range values {
		placeholders[index] = "?"
		args = append(args, value)
	}
	return where + " AND " + column + " IN (" + strings.Join(placeholders, ",") + ")", args
}

func appendTimeRange(where string, args []any, column string, from, to *time.Time) (string, []any) {
	if from != nil {
		where += " AND " + column + ">=?"
		args = append(args, *from)
	}
	if to != nil {
		where += " AND " + column + "<?"
		args = append(args, *to)
	}
	return where, args
}

func (r *Repository) PageVersions(ctx context.Context, tenantID string, input VersionPageInput) (VersionPage, error) {
	page, err := pagination.Normalize(input.Request)
	if err != nil || strings.TrimSpace(input.PolicyID) == "" || len(input.Statuses) > 3 {
		return VersionPage{}, ErrInvalidPolicy
	}
	where := "v.policy_id=? AND v.deleted_at IS NULL AND p.deleted_at IS NULL"
	args := []any{input.PolicyID}
	where, args = visiblePolicyWhere(where, args, tenantID)
	where, args, err = appendEnumFilter(where, args, "v.status", input.Statuses, []string{VersionDraft, VersionPublished, VersionArchived})
	if err != nil {
		return VersionPage{}, err
	}
	from := " FROM data_permission_policy_versions v JOIN data_permission_policies p ON p.id=v.policy_id WHERE " + where
	var total int64
	if err := r.db.GetContext(ctx, &total, r.db.Rebind("SELECT count(*)"+from), args...); err != nil {
		return VersionPage{}, fmt.Errorf("count data permission policy versions: %w", err)
	}
	queryArgs := append(append([]any{}, args...), page.PageSize, pagination.Offset(page))
	items := []VersionRecord{}
	if err := r.db.SelectContext(ctx, &items, r.db.Rebind("SELECT "+prefixedVersionColumns("v")+from+" ORDER BY v.version_number DESC LIMIT ? OFFSET ?"), queryArgs...); err != nil {
		return VersionPage{}, fmt.Errorf("page data permission policy versions: %w", err)
	}
	return VersionPage{Items: items, Page: page.Page, PageSize: page.PageSize, Total: total}, nil
}

func visiblePoliciesWhere(tenantID string) (string, []any) {
	if tenantID == "" {
		return "deleted_at IS NULL AND scope='global'", nil
	}
	return "deleted_at IS NULL AND (scope='global' OR (scope='tenant' AND tenant_id=?))", []any{tenantID}
}

func visiblePolicyQuery(query string, args []any, tenantID string) (string, []any) {
	if tenantID == "" {
		return query + " AND scope='global'", args
	}
	return query + " AND (scope='global' OR (scope='tenant' AND tenant_id=?))", append(args, tenantID)
}

func visiblePolicyWhere(where string, args []any, tenantID string) (string, []any) {
	if tenantID == "" {
		return where + " AND p.scope='global'", args
	}
	return where + " AND (p.scope='global' OR (p.scope='tenant' AND p.tenant_id=?))", append(args, tenantID)
}

func appendEnumFilter(where string, args []any, column string, values, allowed []string) (string, []any, error) {
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

func scopeValues(scopes []PolicyScopeType) []string {
	values := make([]string, len(scopes))
	for index := range scopes {
		values[index] = string(scopes[index])
	}
	return values
}

func prefixedVersionColumns(alias string) string {
	parts := strings.Split(versionColumns, ",")
	for index := range parts {
		parts[index] = alias + "." + parts[index]
	}
	return strings.Join(parts, ",")
}
