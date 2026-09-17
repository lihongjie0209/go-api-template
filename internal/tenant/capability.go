package tenant

import (
	"context"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
)

var ErrCapabilityProviderInvalid = errors.New("tenant capability provider is invalid")

type memberCapabilityProvider struct{ db *sqlx.DB }

func NewMemberCapabilityProvider(db *sqlx.DB) authorization.RowCapabilityProvider {
	return &memberCapabilityProvider{db: db}
}

func (*memberCapabilityProvider) Resource() string { return "tenant.member" }

func (p *memberCapabilityProvider) Load(ctx context.Context, tenantID string, ids []string) (map[string]datapermission.ResourceAttributes, error) {
	if p == nil || p.db == nil || tenantID == "" || len(ids) == 0 {
		return nil, ErrCapabilityProviderInvalid
	}
	query, args, err := sqlx.In(`SELECT id,user_id,status,created_by FROM tenant_memberships WHERE tenant_id=? AND id IN (?) AND deleted_at IS NULL`, tenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("build tenant member capability query: %w", err)
	}
	rows := []struct {
		ID        string `db:"id"`
		UserID    string `db:"user_id"`
		Status    Status `db:"status"`
		CreatedBy string `db:"created_by"`
	}{}
	if err := p.db.SelectContext(ctx, &rows, p.db.Rebind(query), args...); err != nil {
		return nil, fmt.Errorf("load tenant member capabilities: %w", err)
	}
	result := make(map[string]datapermission.ResourceAttributes, len(rows))
	for _, row := range rows {
		result[row.ID] = datapermission.ResourceAttributes{"id": row.ID, "owner_id": row.UserID, "status": string(row.Status), "created_by": row.CreatedBy}
	}
	return result, nil
}

type departmentCapabilityProvider struct{ db *sqlx.DB }

func NewDepartmentCapabilityProvider(db *sqlx.DB) authorization.RowCapabilityProvider {
	return &departmentCapabilityProvider{db: db}
}

func (*departmentCapabilityProvider) Resource() string { return "tenant.department" }

func (p *departmentCapabilityProvider) Load(ctx context.Context, tenantID string, ids []string) (map[string]datapermission.ResourceAttributes, error) {
	if p == nil || p.db == nil || tenantID == "" || len(ids) == 0 {
		return nil, ErrCapabilityProviderInvalid
	}
	query, args, err := sqlx.In(`SELECT id,parent_id,code,name,created_by FROM tenant_departments WHERE tenant_id=? AND id IN (?) AND deleted_at IS NULL`, tenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("build tenant department capability query: %w", err)
	}
	rows := []struct {
		ID        string  `db:"id"`
		ParentID  *string `db:"parent_id"`
		Code      string  `db:"code"`
		Name      string  `db:"name"`
		CreatedBy string  `db:"created_by"`
	}{}
	if err := p.db.SelectContext(ctx, &rows, p.db.Rebind(query), args...); err != nil {
		return nil, fmt.Errorf("load tenant department capabilities: %w", err)
	}
	result := make(map[string]datapermission.ResourceAttributes, len(rows))
	for _, row := range rows {
		parentID := ""
		if row.ParentID != nil {
			parentID = *row.ParentID
		}
		result[row.ID] = datapermission.ResourceAttributes{"id": row.ID, "parent_id": parentID, "code": row.Code, "name": row.Name, "created_by": row.CreatedBy}
	}
	return result, nil
}
