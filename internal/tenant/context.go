package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type AvailableTenant struct {
	TenantID        string    `db:"tenant_id" json:"tenant_id"`
	TenantCode      string    `db:"tenant_code" json:"tenant_code"`
	TenantName      string    `db:"tenant_name" json:"tenant_name"`
	MembershipID    string    `db:"membership_id" json:"membership_id"`
	IsAdministrator bool      `db:"is_administrator" json:"is_administrator"`
	JoinedAt        time.Time `db:"joined_at" json:"joined_at"`
}
type ContextToken struct {
	AccessToken string          `json:"access_token"`
	TokenType   string          `json:"token_type"`
	ExpiresIn   int64           `json:"expires_in"`
	Tenant      AvailableTenant `json:"tenant"`
}
type ContextService struct {
	db       *sqlx.DB
	signer   *auth.Service
	cfg      config.Config
	security securitylog.Recorder
}

func NewContextService(db *sqlx.DB, signer *auth.Service, cfg config.Config, security securitylog.Recorder) *ContextService {
	return &ContextService{db: db, signer: signer, cfg: cfg, security: security}
}
func (s *ContextService) Available(ctx context.Context) ([]AvailableTenant, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.Type != platformprincipal.TypeUser {
		return nil, ErrForbidden
	}
	items := []AvailableTenant{}
	query := `SELECT m.tenant_id,t.code tenant_code,t.name tenant_name,m.id membership_id,CASE WHEN a.id IS NULL THEN false ELSE true END is_administrator,m.joined_at FROM tenant_memberships m JOIN tenants t ON t.id=m.tenant_id AND t.status='active' AND t.deleted_at IS NULL LEFT JOIN tenant_administrators a ON a.tenant_id=m.tenant_id AND a.membership_id=m.id AND a.deleted_at IS NULL WHERE m.user_id=? AND m.status='active' AND m.deleted_at IS NULL ORDER BY t.name,t.id`
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(query), actor.ID); err != nil {
		return nil, err
	}
	return items, nil
}
func (s *ContextService) Switch(ctx context.Context, tenantID string) (ContextToken, error) {
	tenantID = strings.TrimSpace(tenantID)
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.Type != platformprincipal.TypeUser || actor.SessionID == "" || tenantID == "" || len(tenantID) > maxTenantIDLength {
		return ContextToken{}, s.recordSwitch(ctx, actor, tenantID, ErrForbidden)
	}
	var selected AvailableTenant
	query := `SELECT m.tenant_id,t.code tenant_code,t.name tenant_name,m.id membership_id,CASE WHEN a.id IS NULL THEN false ELSE true END is_administrator,m.joined_at FROM tenant_memberships m JOIN tenants t ON t.id=m.tenant_id AND t.status='active' AND t.deleted_at IS NULL LEFT JOIN tenant_administrators a ON a.tenant_id=m.tenant_id AND a.membership_id=m.id AND a.deleted_at IS NULL WHERE m.tenant_id=? AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL`
	err = s.db.GetContext(ctx, &selected, s.db.Rebind(query), tenantID, actor.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return ContextToken{}, s.recordSwitch(ctx, actor, tenantID, ErrForbidden)
	}
	if err != nil {
		return ContextToken{}, s.recordSwitch(ctx, actor, tenantID, err)
	}
	raw, err := s.signer.IssuePrincipal(platformprincipal.Principal{ID: actor.ID, Type: platformprincipal.TypeUser, SessionID: actor.SessionID, TenantID: selected.TenantID, MembershipID: selected.MembershipID})
	if err != nil {
		return ContextToken{}, s.recordSwitch(ctx, actor, tenantID, err)
	}
	if err := s.recordSwitch(ctx, actor, tenantID, nil); err != nil {
		return ContextToken{}, err
	}
	return ContextToken{AccessToken: raw, TokenType: "Bearer", ExpiresIn: int64(s.cfg.JWT.TTL.Seconds()), Tenant: selected}, nil
}

func (s *ContextService) recordSwitch(ctx context.Context, actor platformprincipal.Principal, tenantID string, switchErr error) error {
	if s.security == nil {
		return switchErr
	}
	entry := securitylog.Entry{EventType: securitylog.EventTenantContextSwitch, SubjectID: actor.ID, SubjectType: string(actor.Type), TenantID: tenantID, SessionID: actor.SessionID, Succeeded: switchErr == nil}
	if switchErr != nil {
		entry.ErrorCode = "tenant_context_switch_failed"
		entry.ErrorMessage = "tenant context switch failed"
	}
	if err := s.security.Record(ctx, entry); err != nil && s.security.FailClosed() {
		return fmt.Errorf("%w: %v", ErrSecurityUnavailable, err)
	}
	return switchErr
}
func (s *ContextService) Current(ctx context.Context) (AvailableTenant, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.TenantID == "" || actor.MembershipID == "" {
		return AvailableTenant{}, ErrForbidden
	}
	var selected AvailableTenant
	query := `SELECT m.tenant_id,t.code tenant_code,t.name tenant_name,m.id membership_id,CASE WHEN a.id IS NULL THEN false ELSE true END is_administrator,m.joined_at FROM tenant_memberships m JOIN tenants t ON t.id=m.tenant_id AND t.status='active' AND t.deleted_at IS NULL LEFT JOIN tenant_administrators a ON a.tenant_id=m.tenant_id AND a.membership_id=m.id AND a.deleted_at IS NULL WHERE m.tenant_id=? AND m.id=? AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL`
	if err := s.db.GetContext(ctx, &selected, s.db.Rebind(query), actor.TenantID, actor.MembershipID, actor.ID); err != nil {
		return AvailableTenant{}, ErrForbidden
	}
	return selected, nil
}
