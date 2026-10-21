package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/authentication"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
)

type securityRecorderStub struct {
	entry     securitylog.Entry
	recordErr error
}

func (s *securityRecorderStub) Enabled() bool    { return true }
func (s *securityRecorderStub) FailClosed() bool { return true }
func (s *securityRecorderStub) Record(_ context.Context, entry securitylog.Entry) error {
	s.entry = entry
	return s.recordErr
}
func (s *securityRecorderStub) RecordTx(_ context.Context, _ *sqlx.Tx, entry securitylog.Entry) error {
	s.entry = entry
	return nil
}

func TestAuthenticationFailureCodeDoesNotExposeTechnicalErrors(t *testing.T) {
	t.Parallel()
	if got := authenticationFailureCode(errors.New("postgres password=secret")); got != "internal_error" {
		t.Fatalf("authenticationFailureCode() = %q", got)
	}
	if got := authenticationFailureCode(authentication.ErrAccountLocked); got != "account_locked" {
		t.Fatalf("authenticationFailureCode(account locked) = %q", got)
	}
}

func TestUserAuthenticationRecordFailureDoesNotLogSecurityQueueDetails(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	recorder := &securityRecorderStub{recordErr: errors.New("queue password=secret-value")}
	handler := NewUserAuthenticationHandler(nil, recorder, logger)
	router := gin.New()
	router.Use(RequestID())
	router.POST("/test", func(c *gin.Context) {
		handler.recordFailure(c, authentication.ErrInvalidCredentials, securitylog.Entry{EventType: securitylog.EventLogin}, "login")
		c.Status(http.StatusNoContent)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if strings.Contains(output.String(), "secret-value") || !strings.Contains(output.String(), "security_event_enqueue_failed") {
		t.Fatalf("security record failure log leaked queue detail or omitted safe code: %s", output.String())
	}
}

func TestAuthenticationHandler_LoginRecordsSecurityContextWithoutPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jwtConfig, keyErr := testutil.JWTConfig()
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	authService := auth.New(config.Config{JWT: jwtConfig})
	rawDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })
	db := sqlx.NewDb(rawDB, "pgx")
	hash, err := auth.NewPasswordHasher().Hash("password-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM identity_service_accounts`).WithArgs("client", serviceaccount.StatusActive, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "created_by_name", "updated_at", "updated_by", "updated_by_name", "version", "secret_hash"}).
			AddRow("account-1", "client", "Client", "", serviceaccount.StatusActive, nil, nil, 0, nil, now, "admin", "Administrator", now, "admin", "Administrator", 1, hash))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("account-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_service_accounts SET last_used_at=`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "account-1", "account-1", int64(1), sqlmock.AnyArg(), serviceaccount.StatusActive, sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	recorder := &securityRecorderStub{}
	accounts := serviceaccount.New(db, database.NewTransactor(db), nil, recorder, config.Config{})
	handler := NewAuthenticationHandler(authService, accounts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := gin.New()
	router.Use(RequestID(), SecurityClientContext())
	router.POST("/api/v1/auth/login", handler.Login)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"client_id":"client","client_secret":"password-long-enough"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "frontend-test")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if recorder.entry.EventType != securitylog.EventLogin || !recorder.entry.Succeeded || recorder.entry.SubjectID != "account-1" || recorder.entry.Identifier != "client" || recorder.entry.TokenID == "" {
		t.Fatalf("security entry = %+v", recorder.entry)
	}
	encoded, err := json.Marshal(recorder.entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "access_token") {
		t.Fatalf("security entry leaked credential: %s", encoded)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticationHandler_LoginDistinguishesStoreOutageFromInvalidCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rawDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })
	db := sqlx.NewDb(rawDB, "pgx")
	security := &securityRecorderStub{}
	accounts := serviceaccount.New(db, database.NewTransactor(db), nil, security, config.Config{})
	handler := NewAuthenticationHandler(auth.New(config.Config{}), accounts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := gin.New()
	router.Use(RequestID())
	router.POST("/api/v1/auth/login", handler.Login)
	for _, test := range []struct {
		name   string
		outage bool
		status int
		code   int
	}{
		{name: "database unavailable", outage: true, status: http.StatusServiceUnavailable, code: apperror.CodeDependencyUnavailable},
		{name: "unknown account", status: http.StatusUnauthorized, code: apperror.CodeUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := mock.ExpectQuery(`SELECT .* FROM identity_service_accounts`).WithArgs("billing-worker", serviceaccount.StatusActive, sqlmock.AnyArg())
			if test.outage {
				query.WillReturnError(errors.New("database connection lost"))
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"id"}))
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"client_id":"billing-worker","client_secret":"present-secret"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var body Response
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || body.Code != test.code || body.RequestID == "" || strings.Contains(response.Body.String(), "database connection lost") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUserLoginDistinguishesIdentityStoreOutageFromUnknownUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name   string
		outage bool
		status int
		code   int
		reason string
	}{
		{name: "database unavailable", outage: true, status: http.StatusServiceUnavailable, code: apperror.CodeDependencyUnavailable, reason: "authentication_unavailable"},
		{name: "unknown user", status: http.StatusUnauthorized, code: apperror.CodeUnauthorized, reason: "invalid_credentials"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rawDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rawDB.Close() })
			db := sqlx.NewDb(rawDB, "sqlmock")
			users := identity.New(identity.NewRepository(db), database.NewTransactor(db), nil, nil, nil, nil, config.Config{})
			service := authentication.New(db, database.NewTransactor(db), users, nil, config.Config{})
			security := &securityRecorderStub{}
			handler := NewUserAuthenticationHandler(service, security, slog.New(slog.NewTextHandler(io.Discard, nil)))
			router := gin.New()
			router.Use(RequestID())
			router.POST("/api/v1/auth/user/login", handler.Login)
			query := mock.ExpectQuery(`SELECT .* FROM identity_users u`).WithArgs("alice")
			if test.outage {
				query.WillReturnError(errors.New("database connection lost"))
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"id"}))
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/user/login", strings.NewReader(`{"username":"alice","password":"present-password"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var body Response
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || body.Code != test.code || body.RequestID == "" || security.entry.Reason != test.reason || strings.Contains(response.Body.String(), "database connection lost") {
				t.Fatalf("status=%d body=%s security=%+v", response.Code, response.Body.String(), security.entry)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
