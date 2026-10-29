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

func TestOneRowDistinguishesStaleVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rows    int64
		wantErr bool
	}{
		{name: "updated", rows: 1},
		{name: "stale version", rows: 0, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := oneRow(sqlmock.NewResult(0, tt.rows), nil)
			if errors.Is(err, ErrStaleVersion) != tt.wantErr {
				t.Fatalf("oneRow() error = %v, want stale version %v", err, tt.wantErr)
			}
		})
	}
}

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
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\$1`).WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin-1", "Administrator", now, "admin-1", "Administrator", 1))
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
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\$1`).WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "settles invoices", StatusActive, nil, nil, 0, nil, now, "admin-1", "Administrator", now, "admin-1", "Administrator", 1))
	mock.ExpectCommit()

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

func TestService_CreateRollsBackAndDoesNotDeliverSecretWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := New(db, database.NewTransactor(db), &operationRecorder{}, &securityRecorder{}, config.Config{})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin-1", Type: platformprincipal.TypeUser})
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("admin-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO identity_service_accounts`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\$1`).WithArgs(sqlmock.AnyArg()).WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	created, err := service.Create(ctx, CreateInput{ClientID: "billing-worker", Name: "Billing Worker"})
	if err == nil || !strings.Contains(err.Error(), "result read unavailable") {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Secret != "" {
		t.Fatal("Create() delivered a secret after rollback")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_RotateSecretRollsBackAndDoesNotDeliverSecretWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := New(db, database.NewTransactor(db), &operationRecorder{}, &securityRecorder{}, config.Config{})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin-1", Type: platformprincipal.TypeUser})
	now := time.Now()
	accountRow := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin-1", "Administrator", now, "admin-1", "Administrator", 2)
	}
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\$1`).WithArgs("account-1").WillReturnRows(accountRow())
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("admin-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_service_accounts SET secret_hash=.*,credential_generation=credential_generation\+1`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\$1`).WithArgs("account-1").WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	created, err := service.RotateSecret(ctx, "account-1", 2)
	if err == nil || !strings.Contains(err.Error(), "result read unavailable") {
		t.Fatalf("RotateSecret() error = %v", err)
	}
	if created.Secret != "" {
		t.Fatal("RotateSecret() delivered a secret after rollback")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_RotateSecretDeliversCommittedSecretAndVersion(t *testing.T) {
	t.Parallel()
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
	accountRow := func(version int64) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin-1", "Administrator", now, "admin-1", "Administrator", version)
	}
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\$1`).WithArgs("account-1").WillReturnRows(accountRow(2))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("admin-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_service_accounts SET secret_hash=.*,credential_generation=credential_generation\+1`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\$1`).WithArgs("account-1").WillReturnRows(accountRow(3))
	mock.ExpectCommit()

	created, err := service.RotateSecret(ctx, "account-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Secret) < 40 || created.Account.Version != 3 {
		t.Fatalf("RotateSecret() result = %+v", created.Account)
	}
	payload, err := json.Marshal(struct{ Operation, Security any }{operations.entry, security.entry})
	if err != nil {
		t.Fatal(err)
	}
	if containsCredential(string(payload), created.Secret) {
		t.Fatal("audit payload leaked rotated secret")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_UpdateRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := New(db, database.NewTransactor(db), &operationRecorder{}, &securityRecorder{}, config.Config{})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin-1", Type: platformprincipal.TypeUser})
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("admin-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_service_accounts SET name=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts a WHERE id=\$1`).WithArgs("account-1").WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	_, err = service.Update(ctx, UpdateInput{ID: "account-1", Name: "Billing Worker", Status: StatusActive, Version: 2})
	if err == nil || !strings.Contains(err.Error(), "result read unavailable") {
		t.Fatalf("Update() error = %v", err)
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash", "credential_generation"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin", "Administrator", now, "admin", "Administrator", 1, hash, 1))

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
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash", "credential_generation"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 1, nil, now, "admin", "Administrator", now, "admin", "Administrator", 2, hash, 1))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("account-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_service_accounts SET failed_attempts=failed_attempts\+1.*WHERE id=\$[0-9]+ AND secret_hash=\$[0-9]+ AND status=\$[0-9]+`).
		WithArgs(int64(2), sqlmock.AnyArg(), sqlmock.AnyArg(), "account-1", "account-1", hash, StatusActive, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if _, err := service.Authenticate(t.Context(), "billing-worker", "wrong-secret-value"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_AuthenticateDoesNotCountOldSecretFailureAfterRotation(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	security := &securityRecorder{}
	service := New(db, database.NewTransactor(db), nil, security, config.Config{Authentication: config.Authentication{MaxFailedAttempts: 2, LockDuration: time.Minute}})
	hash, err := service.hasher.Hash("correct-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts`).WithArgs("billing-worker", StatusActive, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash", "credential_generation"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 1, nil, now, "admin", "Administrator", now, "admin", "Administrator", 2, hash, 1))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("account-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_service_accounts SET failed_attempts=failed_attempts\+1.*WHERE id=\$[0-9]+ AND secret_hash=\$[0-9]+ AND status=\$[0-9]+`).
		WithArgs(int64(2), sqlmock.AnyArg(), sqlmock.AnyArg(), "account-1", "account-1", hash, StatusActive, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	if _, err := service.Authenticate(t.Context(), "billing-worker", "wrong-secret-value"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate error = %v, want invalid credentials", err)
	}
	if security.entry.EventType != securitylog.EventLogin || security.entry.Succeeded {
		t.Fatalf("security entry = %+v", security.entry)
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash", "credential_generation"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin", "Administrator", now, "admin", "Administrator", 4, hash, 1))
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash", "credential_generation"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin", "Administrator", now, "admin", "Administrator", 4, hash, 1))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("account-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_service_accounts SET last_used_at=`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "account-1", "account-1", int64(4), hash, StatusActive, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	account, credential, err := service.AuthenticateAndIssue(t.Context(), "billing-worker", "correct-secret-value", func(string, int64) (string, string, error) {
		return "issued-but-not-returned", "token-1", nil
	})
	if !errors.Is(err, ErrSecurityUnavailable) || account.ID != "" || credential != "" {
		t.Fatalf("AuthenticateAndIssue() account=%+v credential=%q error=%v", account, credential, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_AuthenticateDistinguishesLookupFailureFromUnknownAccount(t *testing.T) {
	for _, test := range []struct {
		name       string
		storeError bool
	}{
		{name: "database unavailable", storeError: true},
		{name: "unknown account"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "pgx")
			security := &securityRecorder{}
			service := New(db, database.NewTransactor(db), nil, security, config.Config{})
			query := mock.ExpectQuery(`SELECT .* FROM identity_service_accounts`).WithArgs("billing-worker", StatusActive, sqlmock.AnyArg())
			if test.storeError {
				query.WillReturnError(errors.New("database connection lost"))
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"id"}))
			}
			_, err = service.Authenticate(t.Context(), "billing-worker", "present-secret")
			if test.storeError && (!errors.Is(err, ErrAuthenticationUnavailable) || errors.Is(err, ErrInvalidCredentials)) {
				t.Fatalf("Authenticate error = %v, want dependency unavailable", err)
			}
			if !test.storeError && (!errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrAuthenticationUnavailable)) {
				t.Fatalf("Authenticate error = %v, want invalid credentials", err)
			}
			if security.entry.EventType != securitylog.EventLogin || security.entry.Succeeded {
				t.Fatalf("security entry = %+v", security.entry)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestService_AuthenticateDistinguishesSuccessWriteFailure(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := New(db, database.NewTransactor(db), nil, &securityRecorder{}, config.Config{})
	hash, err := service.hasher.Hash("correct-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts`).WithArgs("billing-worker", StatusActive, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash", "credential_generation"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin", "Administrator", now, "admin", "Administrator", 4, hash, 1))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("account-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_service_accounts SET last_used_at=`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "account-1", "account-1", int64(4), hash, StatusActive, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(errors.New("database write lost"))
	mock.ExpectRollback()
	account, token, err := service.AuthenticateAndIssue(t.Context(), "billing-worker", "correct-secret-value", func(string, int64) (string, string, error) {
		return "issued-but-not-returned", "token-1", nil
	})
	if !errors.Is(err, ErrAuthenticationUnavailable) || account.ID != "" || token != "" {
		t.Fatalf("AuthenticateAndIssue account=%+v token=%q error=%v", account, token, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_AuthenticateDistinguishesIssuerFailure(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	security := &securityRecorder{}
	service := New(db, database.NewTransactor(db), nil, security, config.Config{})
	hash, err := service.hasher.Hash("correct-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts`).WithArgs("billing-worker", StatusActive, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash", "credential_generation"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin", "Administrator", now, "admin", "Administrator", 4, hash, 1))
	account, token, err := service.AuthenticateAndIssue(t.Context(), "billing-worker", "correct-secret-value", func(string, int64) (string, string, error) {
		return "", "", errors.New("signing key unavailable")
	})
	if !errors.Is(err, ErrAuthenticationUnavailable) || account.ID != "" || token != "" {
		t.Fatalf("AuthenticateAndIssue account=%+v token=%q error=%v", account, token, err)
	}
	if security.entry.Reason != "credential_issue_failed" || security.entry.Succeeded {
		t.Fatalf("security entry = %+v", security.entry)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_AuthenticateRejectsCorruptStoredHashAsUnavailable(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	security := &securityRecorder{}
	service := New(db, database.NewTransactor(db), nil, security, config.Config{})
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts`).WithArgs("billing-worker", StatusActive, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash", "credential_generation"}).
			AddRow("account-1", "billing-worker", "Billing Worker", "", StatusActive, nil, nil, 0, nil, now, "admin", "Administrator", now, "admin", "Administrator", 4, "not-a-valid-argon2-hash", 1))
	_, err = service.Authenticate(t.Context(), "billing-worker", "present-secret")
	if !errors.Is(err, ErrAuthenticationUnavailable) || errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate error = %v, want unavailable for stored-hash corruption", err)
	}
	if security.entry.Reason != "credential_verification_failed" || security.entry.Succeeded {
		t.Fatalf("security entry = %+v", security.entry)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func containsCredential(payload, secret string) bool {
	return strings.Contains(payload, secret) || strings.Contains(payload, "secret_hash")
}
