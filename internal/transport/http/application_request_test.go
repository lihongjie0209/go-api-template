package httptransport

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestApplicationPageOptionalFilters(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "no filters", body: `{}`},
		{name: "empty filters", body: `{"ids":[],"codes":[]}`},
		{name: "valid filters", body: `{"ids":["app-1"],"codes":["identity"]}`},
		{name: "blank id", body: `{"ids":[""]}`, wantErr: true},
		{name: "blank code", body: `{"codes":[""]}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", "/api/v1/applications/page", strings.NewReader(tt.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			var request ApplicationPageRequest
			err := ctx.ShouldBindJSON(&request)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ShouldBindJSON() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTenantApplicationPageOptionalFilter(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "no application IDs", body: `{"tenant_id":"tenant-1"}`},
		{name: "empty application IDs", body: `{"tenant_id":"tenant-1","application_ids":[]}`},
		{name: "valid application IDs", body: `{"tenant_id":"tenant-1","application_ids":["app-1"]}`},
		{name: "blank application ID", body: `{"tenant_id":"tenant-1","application_ids":[""]}`, wantErr: true},
		{name: "missing tenant ID", body: `{}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", "/api/v1/tenant-applications/page", strings.NewReader(tt.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			var request TenantApplicationPageRequest
			err := ctx.ShouldBindJSON(&request)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ShouldBindJSON() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
