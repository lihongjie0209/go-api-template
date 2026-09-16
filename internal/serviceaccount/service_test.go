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
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type operationRecorder struct{ entry operationlog.Entry }

func (r *operationRecorder) Enabled() bool { return true }
func (r *operationRecorder) Record(_ context.Context, entry operationlog.Entry) error {
	r.entry = entry
	return nil
}
func (r *operationRecorder) RecordTx(_ context.Context, _ *sqlx.Tx, entry operationlog.Entry) error {
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
func (r *securityRecorder) RecordTx(_ context.Context, _ *sqlx.Tx, entry securitylog.Entry) error {
	r.entry = entry
	return nil
}

type failingTransactionalSecurityRecorder struct {
	txErr      error
	standalone []securitylog.Entry
}

func (*failingTransactionalSecurityRecorder) Enabled() bool    { return true }
func (*failingTransactionalSecurityRecorder) FailClosed() bool { return true }
func (r *failingTransactionalSecurityRecorder) Record(_ context.Context, entry securitylog.Entry) error {
	r.standalone = append(r.standalone, entry)
	return nil
}
func (r *failingTransactionalSecurityRecorder) RecordTx(context.Context, *sqlx.Tx, securitylog.Entry) error {
	return r.txErr
}

func TestService_CreateRollsBackWhenTransactionalSecurityLogFails(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	operations := &operationRecorder{}
	security := &failingTransactionalSecurityRecorder{txErr: errors.New("security outbox unavailable")}
	service := New(db, database.NewTransactor(db), operations, security, config.Config{})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin-1", Type: platformprincipal.TypeUser})

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("admin-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO identity_service_accounts`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectRollback()

	_, err = service.Create(ctx, CreateInput{ClientID: "billing-worker", Name: "Billing Worker"})
	if err == nil || !strings.Contains(err.Error(), "security outbox unavailable") {
		t.Fatalf("Create() error = %v", err)
	}
	if operations.entry.Succeeded {
		t.Fatalf("operation failure entry = %+v", operations.entry)
	}
	if len(security.standalone) != 1 || security.standalone[0].Succeeded {
		t.Fatalf("security failure entries = %+v", security.standalone)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
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
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\$1`).WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "settles invoices", StatusActive, nil, nil, 0, nil, now, "admin-1", "Administrator", now, "admin-1", "Administrator", 1))

	created, err := service.Create(ctx, CreateInput{ClientID: " Billing-Worker ", Name: "Billing Worker", Description: "settles invoices"})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Secret) < 40 || created.Account.ClientID != "billing-worker" || created.Account.CreatedByName != "Administrator" || created.Account.UpdatedByName != "Administrator" {
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin", "Administrator", now, "admin", "Administrator", 1, hash))

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
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 1, nil, now, "admin", "Administrator", now, "admin", "Administrator", 2, hash))
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

func TestServiceAccountInputsRejectUnboundedFiltersBeforeDatabase(t *testing.T) {
	t.Parallel()
	service := &Service{}
	ctx := platformprincipal.SystemContext(t.Context(), "tester")
	if _, err := service.Page(ctx, PageInput{Request: pagination.Request{Keyword: strings.Repeat("x", maxAccountKeyword+1)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Page() error = %v", err)
	}
	if _, err := service.Page(ctx, PageInput{ClientIDs: []string{"INVALID CLIENT"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Page() invalid client error = %v", err)
	}
	if _, err := service.Get(ctx, strings.Repeat("x", maxAccountIDLength+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestService_AuthenticateRejectsCredentialChangedAfterVerification(t *testing.T) {
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin", "Administrator", now, "admin", "Administrator", 4, hash))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("account-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_service_accounts SET last_used_at=`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "account-1", "account-1", int64(4), hash, StatusActive, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	if _, err := service.Authenticate(t.Context(), "billing-worker", "correct-secret-value"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_AuthenticateRollsBackWhenSecurityEventCannotBeStored(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	security := &failingTransactionalSecurityRecorder{txErr: errors.New("security outbox unavailable")}
	service := New(db, database.NewTransactor(db), &operationRecorder{}, security, config.Config{})
	hash, err := service.hasher.Hash("correct-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts`).WithArgs("billing-worker", StatusActive, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin", "Administrator", now, "admin", "Administrator", 4, hash))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("account-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_service_accounts SET last_used_at=`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "account-1", "account-1", int64(4), hash, StatusActive, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	account, credential, err := service.AuthenticateAndIssue(t.Context(), "billing-worker", "correct-secret-value", func(string) (string, string, error) {
		return "issued-but-not-returned", "token-1", nil
	})
	if !errors.Is(err, ErrSecurityUnavailable) || account.ID != "" || credential != "" {
		t.Fatalf("AuthenticateAndIssue() account=%+v credential=%q error=%v", account, credential, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func containsCredential(payload, secret string) bool {
	return strings.Contains(payload, secret) || strings.Contains(payload, "secret_hash")
}
