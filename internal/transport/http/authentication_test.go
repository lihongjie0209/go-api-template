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
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/authentication"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
)

type securityRecorderStub struct{ entry securitylog.Entry }

func (s *securityRecorderStub) Enabled() bool    { return true }
func (s *securityRecorderStub) FailClosed() bool { return true }
func (s *securityRecorderStub) Record(_ context.Context, entry securitylog.Entry) error {
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "description", "status", "expires_at", "last_used_at", "failed_attempts", "locked_until", "created_at", "created_by", "updated_at", "updated_by", "version", "secret_hash"}).
			AddRow("account-1", "client", "Client", "", serviceaccount.StatusActive, nil, nil, 0, nil, now, "admin", now, "admin", 1, hash))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("account-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE identity_service_accounts SET last_used_at=`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "account-1", "account-1", int64(1), sqlmock.AnyArg(), serviceaccount.StatusActive, sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	accounts := serviceaccount.New(db, database.NewTransactor(db), nil, nil, config.Config{})
	recorder := &securityRecorderStub{}
	handler := NewAuthenticationHandler(authService, accounts, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := gin.New()
	router.Use(RequestID())
	router.POST("/api/v1/auth/login", handler.Login)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"client_id":"client","client_secret":"password-long-enough"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "frontend-test")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if recorder.entry.EventType != securitylog.EventLogin || !recorder.entry.Succeeded || recorder.entry.SubjectID != "account-1" || recorder.entry.Identifier != "client" || recorder.entry.UserAgent != "frontend-test" || recorder.entry.ClientIP == "" {
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
