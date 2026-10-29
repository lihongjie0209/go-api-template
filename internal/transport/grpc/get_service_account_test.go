package grpctransport

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	identityv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/identity/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIdentityGetServiceAccount(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name      string
		id        string
		principal platformprincipal.Principal
		rows      *sqlmock.Rows
		queryErr  error
		wantCode  codes.Code
		wantOn    bool
		query     bool
	}{
		{name: "active", id: "account-1", principal: servicePrincipal(), rows: serviceAccountRows("active", nil, nil), wantOn: true, query: true},
		{name: "disabled", id: "account-1", principal: servicePrincipal(), rows: serviceAccountRows("disabled", nil, nil), query: true},
		{name: "expired", id: "account-1", principal: servicePrincipal(), rows: serviceAccountRows("active", timePointer(now.Add(-time.Hour)), nil), query: true},
		{name: "locked", id: "account-1", principal: servicePrincipal(), rows: serviceAccountRows("active", nil, timePointer(now.Add(time.Hour))), query: true},
		{name: "missing", id: "account-1", principal: servicePrincipal(), rows: sqlmock.NewRows([]string{"id", "name", "status", "expires_at", "locked_until"}), wantCode: codes.NotFound, query: true},
		{name: "invalid id", principal: servicePrincipal(), wantCode: codes.InvalidArgument},
		{name: "missing principal", id: "account-1", wantCode: codes.Unauthenticated},
		{name: "user principal", id: "account-1", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"}, wantCode: codes.PermissionDenied},
		{name: "store failure", id: "account-1", principal: servicePrincipal(), queryErr: errors.New("private connection string"), wantCode: codes.Unavailable, query: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			if tt.query {
				query := mock.ExpectQuery(`FROM identity_service_accounts a WHERE id=\? AND deleted_at IS NULL`).WithArgs(tt.id)
				if tt.queryErr != nil {
					query.WillReturnError(tt.queryErr)
				} else {
					query.WillReturnRows(tt.rows)
				}
			}
			server := &identityServer{accounts: serviceaccount.New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, config.Config{}), jwtAudience: "platform-api"}
			ctx := t.Context()
			if tt.principal.ID != "" {
				ctx = platformprincipal.WithContext(ctx, tt.principal)
			}
			response, err := server.GetServiceAccount(ctx, &identityv1.GetServiceAccountRequest{ServiceAccountId: tt.id})
			if code := status.Code(err); code != tt.wantCode {
				t.Fatalf("code=%v, want %v: %v", code, tt.wantCode, err)
			}
			if tt.wantCode == codes.OK {
				if response.GetId() != "account-1" || response.GetName() != "Worker" || response.GetEnabled() != tt.wantOn || len(response.GetAudiences()) != 1 || response.GetAudiences()[0] != "platform-api" {
					t.Fatalf("response=%+v, want enabled=%v with platform audience", response, tt.wantOn)
				}
			}
			if tt.queryErr != nil && strings.Contains(err.Error(), tt.queryErr.Error()) {
				t.Fatal("database details leaked to caller")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIdentityGetServiceAccountMissingDependency(t *testing.T) {
	t.Parallel()
	ctx := platformprincipal.WithContext(t.Context(), servicePrincipal())
	_, err := (&identityServer{}).GetServiceAccount(ctx, &identityv1.GetServiceAccountRequest{ServiceAccountId: "account-1"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code=%v, want Unavailable", status.Code(err))
	}
}

func servicePrincipal() platformprincipal.Principal {
	return platformprincipal.Principal{ID: "caller", Type: platformprincipal.TypeServiceAccount}
}

func serviceAccountRows(accountStatus string, expiresAt, lockedUntil *time.Time) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "name", "status", "expires_at", "locked_until"}).AddRow("account-1", "Worker", accountStatus, expiresAt, lockedUntil)
}

func timePointer(value time.Time) *time.Time { return &value }
