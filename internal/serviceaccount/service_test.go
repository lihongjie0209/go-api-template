package serviceaccount

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type operationRecorder struct{ entry operationlog.Entry }

func (r *operationRecorder) Enabled() bool { return true }
func (r *operationRecorder) Record(_ context.Context, entry operationlog.Entry) error {
	r.entry = entry
	return nil
}

type securityRecorder struct{ entry securitylog.Entry }

func (r *securityRecorder) Enabled() bool    { return true }
func (r *securityRecorder) FailClosed() bool { return true }
func (r *securityRecorder) Record(_ context.Context, entry securitylog.Entry) error {
	r.entry = entry
	return nil
}

func TestService_CreateReturnsSecretOnceWithoutAuditingIt(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	operations, security := &operationRecorder{}, &securityRecorder{}
	service := New(db, database.NewTransactor(db), operations, security, config.Config{})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin-1", Type: platformprincipal.TypeUser})
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("admin-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO identity_service_accounts`).
		WithArgs(sqlmock.AnyArg(), "billing-worker", "Billing Worker", "settles invoices", sqlmock.AnyArg(), StatusActive, nil, 0, sqlmock.AnyArg(), "admin-1", sqlmock.AnyArg(), "admin-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts WHERE id=\$1`).WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "settles invoices", StatusActive, nil, nil, 0, nil, now, "admin-1", now, "admin-1", 1))

	created, err := service.Create(ctx, CreateInput{ClientID: " Billing-Worker ", Name: "Billing Worker", Description: "settles invoices"})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Secret) < 40 || created.Account.ClientID != "billing-worker" {
		t.Fatalf("created account = %+v", created)
	}
	payload, err := json.Marshal(struct {
		Operation any
		Security  any
	}{operations.entry, security.entry})
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) == "" || containsCredential(string(payload), created.Secret) {
		t.Fatalf("audit payload leaked generated secret: %s", payload)
	}
	if security.entry.EventType != securitylog.EventServiceAccountChanged || !security.entry.Succeeded {
		t.Fatalf("security entry = %+v", security.entry)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_AuthenticateRejectsInvalidSecretWithoutLastUsedWrite(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := New(db, database.NewTransactor(db), &operationRecorder{}, &securityRecorder{}, config.Config{})
	hash, err := service.hasher.Hash("correct-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts`).WithArgs("billing-worker", StatusActive, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "updated_at", "updated_by", "version", "secret_hash"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin", now, "admin", 1, hash))

	if _, err := service.Authenticate(t.Context(), "billing-worker", "wrong-secret-value"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_AuthenticateAtomicallyTracksFailedAttempts(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := New(db, database.NewTransactor(db), &operationRecorder{}, &securityRecorder{}, config.Config{Authentication: config.Authentication{MaxFailedAttempts: 2, LockDuration: time.Minute}})
	hash, err := service.hasher.Hash("correct-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts`).WithArgs("billing-worker", StatusActive, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "updated_at", "updated_by", "version", "secret_hash"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 1, nil, now, "admin", now, "admin", 2, hash))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("account-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_service_accounts SET failed_attempts=failed_attempts\+1`).
		WithArgs(int64(2), sqlmock.AnyArg(), sqlmock.AnyArg(), "account-1", "account-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if _, err := service.Authenticate(t.Context(), "billing-worker", "wrong-secret-value"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func containsCredential(payload, secret string) bool {
	return strings.Contains(payload, secret) || strings.Contains(payload, "secret_hash")
}
