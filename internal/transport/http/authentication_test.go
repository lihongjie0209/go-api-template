package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
)

type securityRecorderStub struct{ entry securitylog.Entry }

func (s *securityRecorderStub) Enabled() bool    { return true }
func (s *securityRecorderStub) FailClosed() bool { return true }
func (s *securityRecorderStub) Record(_ context.Context, entry securitylog.Entry) error {
	s.entry = entry
	return nil
}

func TestAuthenticationHandler_LoginRecordsSecurityContextWithoutPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jwtConfig, keyErr := testutil.JWTConfig()
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	authService := auth.New(config.Config{JWT: jwtConfig, Auth: config.Auth{ClientID: "client", ClientSecret: "password"}})
	recorder := &securityRecorderStub{}
	handler := NewAuthenticationHandler(authService, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := gin.New()
	router.Use(RequestID())
	router.POST("/api/v1/auth/login", handler.Login)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"client_id":"client","client_secret":"password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "frontend-test")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if recorder.entry.EventType != securitylog.EventLogin || !recorder.entry.Succeeded || recorder.entry.SubjectID != "client" || recorder.entry.Identifier != "client" || recorder.entry.UserAgent != "frontend-test" || recorder.entry.ClientIP == "" {
		t.Fatalf("security entry = %+v", recorder.entry)
	}
	encoded, err := json.Marshal(recorder.entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "access_token") {
		t.Fatalf("security entry leaked credential: %s", encoded)
	}
}
