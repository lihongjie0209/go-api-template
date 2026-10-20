package httptransport

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/files"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
	"github.com/lihongjie0209/go-api-template/internal/tenant"
)

func TestFileVersionConflictUsesStaleVersionCode(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/files/delete", nil)
	handler := &FileHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	handler.fail(context, files.ErrVersionConflict)

	var response struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusConflict || response.Code != apperror.CodeStaleVersion {
		t.Fatalf("file version conflict status=%d code=%d", recorder.Code, response.Code)
	}
}

func TestTenantStaleVersionUsesDedicatedCode(t *testing.T) {
	for _, test := range []struct {
		name string
		fail func(*gin.Context, error)
	}{
		{"tenant", (&TenantHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).fail},
		{"member", (&TenantMemberHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).fail},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenants/update", nil)
			test.fail(context, tenant.ErrStaleVersion)
			var response struct {
				Code int `json:"code"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusConflict || response.Code != apperror.CodeStaleVersion {
				t.Fatalf("stale version status=%d code=%d", recorder.Code, response.Code)
			}
		})
	}
}

func TestTenantBusinessConflictKeepsGenericCode(t *testing.T) {
	for _, test := range []struct {
		name string
		fail func(*gin.Context, error)
	}{
		{"tenant", (&TenantHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).fail},
		{"member", (&TenantMemberHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).fail},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenants/update", nil)
			test.fail(context, tenant.ErrConflict)
			var response struct {
				Code int `json:"code"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusConflict || response.Code != apperror.CodeConflict {
				t.Fatalf("business conflict status=%d code=%d", recorder.Code, response.Code)
			}
		})
	}
}

func TestServiceAccountStaleVersionUsesDedicatedCode(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts/secret/rotate", nil)
	handler := &ServiceAccountHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	handler.fail(context, serviceaccount.ErrStaleVersion)

	var response struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusConflict || response.Code != apperror.CodeStaleVersion {
		t.Fatalf("service account stale version status=%d code=%d", recorder.Code, response.Code)
	}
}

func TestUserStaleVersionUsesDedicatedCode(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/users/update", nil)
	handler := &UserHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	handler.fail(context, identity.ErrStaleVersion)

	var response struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusConflict || response.Code != apperror.CodeStaleVersion {
		t.Fatalf("user stale version status=%d code=%d", recorder.Code, response.Code)
	}
}
