package httptransport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
)

type securityLogServiceStub struct {
	record    securitylog.Record
	recorded  securitylog.Entry
	recordErr error
}

func (*securityLogServiceStub) Enabled() bool { return true }

func (s *securityLogServiceStub) Get(context.Context, string) (securitylog.Record, error) {
	return s.record, nil
}

func (*securityLogServiceStub) Page(context.Context, securitylog.PageInput) (securitylog.Page, error) {
	return securitylog.Page{}, nil
}

func (s *securityLogServiceStub) Record(_ context.Context, entry securitylog.Entry) error {
	s.recorded = entry
	return s.recordErr
}

func TestSecurityLogGetRecordsAccessBeforeReturningSensitiveData(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	service := &securityLogServiceStub{record: securitylog.Record{ID: "security-1"}}
	router := securityLogTestRouter(service)

	request := httptest.NewRequest(http.MethodPost, "/security/get", strings.NewReader(`{"id":"security-1"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.recorded.EventType != securitylog.EventSecurityLogAccess || service.recorded.SubjectID != "security-1" || service.recorded.SubjectType != "security_log" || !service.recorded.Succeeded {
		t.Fatalf("access entry = %+v", service.recorded)
	}
}

func TestSecurityLogGetFailsClosedWhenAccessAuditCannotBeStored(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	service := &securityLogServiceStub{record: securitylog.Record{ID: "security-1"}, recordErr: errors.New("outbox unavailable")}
	router := securityLogTestRouter(service)

	request := httptest.NewRequest(http.MethodPost, "/security/get", strings.NewReader(`{"id":"security-1"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "security-1") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func securityLogTestRouter(service securityLogService) *gin.Engine {
	handler := &SecurityLogHandler{service: service, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router := gin.New()
	router.Use(RequestID())
	router.POST("/security/get", handler.Get)
	return router
}
