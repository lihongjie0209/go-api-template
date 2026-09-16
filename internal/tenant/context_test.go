package tenant

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestContextService_AvailableScopesMembershipsToAuthenticatedUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	service := &ContextService{db: sqlxDB}
	mock.ExpectQuery(`FROM tenant_memberships m JOIN tenants`).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "tenant_code", "tenant_name", "membership_id", "is_administrator", "joined_at"}))
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	items, err := service.Available(ctx)
	require.NoError(t, err)
	require.NotNil(t, items)
	require.Empty(t, items)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContextService_CurrentRequiresActiveTenantAndMembership(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := &ContextService{db: sqlx.NewDb(db, "sqlmock")}
	mock.ExpectQuery(`JOIN tenants t ON t.id=m.tenant_id AND t.status='active'.*WHERE m.tenant_id=\? AND m.id=\? AND m.user_id=\? AND m.status='active'`).WithArgs("tenant-1", "membership-1", "user-1").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "tenant_code", "tenant_name", "membership_id", "is_administrator", "joined_at"}))
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1"})
	_, err = service.Current(ctx)
	require.ErrorIs(t, err, ErrForbidden)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContextService_AvailableRejectsServiceAccount(t *testing.T) {
	service := &ContextService{}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount})
	_, err := service.Available(ctx)
	require.ErrorIs(t, err, ErrForbidden)
}

func TestContextService_SwitchRecordsRejectedAttemptAtServiceBoundary(t *testing.T) {
	security := &transactionalSecurityRecorderStub{}
	service := &ContextService{security: security}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount})

	_, err := service.Switch(ctx, "tenant-1")
	require.ErrorIs(t, err, ErrForbidden)
	require.Len(t, security.standalone, 1)
	entry := security.standalone[0]
	require.Equal(t, securitylog.EventTenantContextSwitch, entry.EventType)
	require.Equal(t, "service-1", entry.SubjectID)
	require.Equal(t, "tenant-1", entry.TenantID)
	require.False(t, entry.Succeeded)
}

func TestContextService_SwitchFailsClosedWhenSecurityAuditIsUnavailable(t *testing.T) {
	wantErr := errors.New("security store unavailable")
	security := &transactionalSecurityRecorderStub{recordErr: wantErr}
	service := &ContextService{security: security}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount})

	_, err := service.Switch(ctx, "tenant-1")
	require.ErrorIs(t, err, ErrSecurityUnavailable)
	require.NotErrorIs(t, err, ErrForbidden)
}

func TestContextService_SwitchDoesNotReturnIssuedTokenWhenAuditFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	mock.ExpectQuery(`FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "user-1").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "tenant_code", "tenant_name", "membership_id", "is_administrator", "joined_at"}).AddRow("tenant-1", "tenant", "Tenant", "membership-1", true, now))
	wantErr := errors.New("security store unavailable")
	security := &transactionalSecurityRecorderStub{recordErr: wantErr}
	cfg := contextSigningConfig(t)
	service := NewContextService(sqlx.NewDb(db, "sqlmock"), auth.New(cfg), cfg, security)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"})

	token, err := service.Switch(ctx, "tenant-1")
	require.ErrorIs(t, err, ErrSecurityUnavailable)
	require.Empty(t, token.AccessToken)
	require.Len(t, security.standalone, 1)
	require.True(t, security.standalone[0].Succeeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func contextSigningConfig(t *testing.T) config.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return config.Config{JWT: config.JWT{Issuer: "identity", Audience: "platform", Algorithm: "ES256", KeyID: "test-key", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), TTL: time.Hour}}
}
