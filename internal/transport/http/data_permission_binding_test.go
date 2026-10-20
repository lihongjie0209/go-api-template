package httptransport

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

func TestDataPermissionSimulationContextUsesAuthenticatedTenant(t *testing.T) {
	for _, mode := range []struct {
		name string
		path string
		run  func(*DataPermissionHandler, *gin.Context)
	}{
		{name: "simulate", path: "/api/v1/data-permissions/tenant-policies/simulate/context", run: (*DataPermissionHandler).SimulationContext},
		{name: "create", path: "/api/v1/data-permissions/tenant-policies/create/context", run: (*DataPermissionHandler).CreateContext},
	} {
		tests := []struct {
			name       string
			principal  *platformprincipal.Principal
			wantStatus int
			wantTenant string
		}{
			{name: "tenant principal", principal: &platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"}, wantStatus: http.StatusOK, wantTenant: "tenant-1"},
			{name: "no tenant", principal: &platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}, wantStatus: http.StatusForbidden},
			{name: "no principal", wantStatus: http.StatusUnauthorized},
		}
		for _, test := range tests {
			t.Run(mode.name+"/"+test.name, func(t *testing.T) {
				writer := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(writer)
				request := httptest.NewRequest(http.MethodPost, mode.path, strings.NewReader(`{}`))
				if test.principal != nil {
					request = request.WithContext(platformprincipal.WithContext(t.Context(), *test.principal))
				}
				ctx.Request = request
				mode.run(&DataPermissionHandler{logger: slog.Default()}, ctx)
				if writer.Code != test.wantStatus {
					t.Fatalf("status = %d, want %d; body = %s", writer.Code, test.wantStatus, writer.Body.String())
				}
				var response struct {
					Body DataPermissionSimulationContext `json:"body"`
				}
				if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Body.TenantID != test.wantTenant {
					t.Fatalf("tenant = %q, want %q", response.Body.TenantID, test.wantTenant)
				}
			})
		}
	}
}

func TestTenantSimulationRequiresTheCurrentPrincipalTenant(t *testing.T) {
	tests := []struct {
		name      string
		principal *platformprincipal.Principal
		ids       []string
		want      bool
	}{
		{name: "matching policy subject and resource", principal: &platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"}, ids: []string{"tenant-1", "tenant-1", "tenant-1"}, want: true},
		{name: "different resource tenant", principal: &platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"}, ids: []string{"tenant-1", "tenant-1", "tenant-2"}},
		{name: "no active tenant", principal: &platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}, ids: []string{"tenant-1", "tenant-1", "tenant-1"}},
		{name: "no principal", ids: []string{"tenant-1", "tenant-1", "tenant-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.POST("/api/v1/data-permissions/tenant-policies/simulate", func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"allowed": simulationTenantAllowed(c, test.ids...)})
			})
			request := httptest.NewRequest(http.MethodPost, "/api/v1/data-permissions/tenant-policies/simulate", strings.NewReader(`{}`))
			if test.principal != nil {
				request = request.WithContext(platformprincipal.WithContext(t.Context(), *test.principal))
			}
			writer := httptest.NewRecorder()
			router.ServeHTTP(writer, request)
			var response struct {
				Allowed bool `json:"allowed"`
			}
			if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Allowed != test.want {
				t.Fatalf("allowed = %v, want %v", response.Allowed, test.want)
			}
		})
	}
}

func TestDataPermissionStatusTargetContainsOnlyConfirmationAndVersion(t *testing.T) {
	policy := datapermission.PolicyRecord{
		ID: "policy-1", Code: "member-owner", Name: "Member owner",
		Scope: datapermission.PolicyScopeTenant, Status: datapermission.StatusActive,
		Version: 7, CreatedBy: "creator-id", UpdatedBy: "updater-id",
	}
	statusTarget, err := dataPermissionStatusTarget(policy, datapermission.PolicyScopeTenant)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(statusTarget)
	if err != nil {
		t.Fatal(err)
	}
	var target map[string]any
	if err := json.Unmarshal(encoded, &target); err != nil {
		t.Fatal(err)
	}
	if len(target) != 6 || target["id"] != policy.ID || target["code"] != policy.Code || target["name"] != policy.Name || target["scope"] != string(policy.Scope) || target["status"] != policy.Status || target["version"] != float64(policy.Version) {
		t.Fatalf("unexpected status target: %s", encoded)
	}
	for _, field := range []string{"created_by", "updated_by", "tenant_id", "published_version_number"} {
		if _, exists := target[field]; exists {
			t.Fatalf("status target leaked %s: %s", field, encoded)
		}
	}
}

func TestDataPermissionStatusTargetRejectsAnotherScope(t *testing.T) {
	policy := datapermission.PolicyRecord{ID: "policy-1", Scope: datapermission.PolicyScopeTenant}
	if _, err := dataPermissionStatusTarget(policy, datapermission.PolicyScopeGlobal); err != datapermission.ErrPolicyNotFound {
		t.Fatalf("wrong-scope status target error = %v, want not found", err)
	}
}

func TestDataPermissionPublishTarget(t *testing.T) {
	policy := datapermission.PolicyRecord{
		ID: "policy-1", Code: "member-owner", Name: "Member owner",
		Scope: datapermission.PolicyScopeTenant, Status: datapermission.StatusActive,
		Version: 7, CreatedBy: "creator-id",
	}
	version := datapermission.VersionRecord{
		PolicyID: policy.ID, VersionNumber: 3,
		Status: datapermission.VersionDraft, Document: "sensitive policy document",
	}
	target, err := dataPermissionPublishTarget(policy, version, datapermission.PolicyScopeTenant, 3)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 8 || fields["id"] != policy.ID || fields["name"] != policy.Name ||
		fields["version"] != float64(7) || fields["version_number"] != float64(3) ||
		fields["version_status"] != datapermission.VersionDraft {
		t.Fatalf("unexpected publish target: %s", encoded)
	}
	for _, field := range []string{"document", "created_by", "updated_by", "tenant_id"} {
		if _, exists := fields[field]; exists {
			t.Fatalf("publish target leaked %s: %s", field, encoded)
		}
	}
}

func TestDataPermissionPublishTargetRejectsWrongScopeVersionAndStatus(t *testing.T) {
	policy := datapermission.PolicyRecord{ID: "policy-1", Scope: datapermission.PolicyScopeTenant}
	version := datapermission.VersionRecord{PolicyID: policy.ID, VersionNumber: 3, Status: datapermission.VersionDraft}
	tests := []struct {
		name    string
		policy  datapermission.PolicyRecord
		version datapermission.VersionRecord
		number  int64
		scope   datapermission.PolicyScopeType
		want    error
	}{
		{name: "wrong scope", policy: policy, version: version, number: 3, scope: datapermission.PolicyScopeGlobal, want: datapermission.ErrPolicyNotFound},
		{name: "wrong owner", policy: policy, version: datapermission.VersionRecord{PolicyID: "other", VersionNumber: 3, Status: datapermission.VersionDraft}, number: 3, scope: datapermission.PolicyScopeTenant, want: datapermission.ErrPolicyNotFound},
		{name: "wrong number", policy: policy, version: version, number: 4, scope: datapermission.PolicyScopeTenant, want: datapermission.ErrPolicyNotFound},
		{name: "published version", policy: policy, version: datapermission.VersionRecord{PolicyID: policy.ID, VersionNumber: 3, Status: datapermission.VersionPublished}, number: 3, scope: datapermission.PolicyScopeTenant, want: datapermission.ErrVersionNotDraft},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := dataPermissionPublishTarget(test.policy, test.version, test.scope, test.number); err != test.want {
				t.Fatalf("publish target error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDataPermissionVersionCreateTarget(t *testing.T) {
	policy := datapermission.PolicyRecord{
		ID: "policy-1", Code: "member-owner", Name: "Member owner",
		Scope: datapermission.PolicyScopeTenant, TenantID: sql.NullString{String: "tenant-1", Valid: true},
		Version: 7, CreatedBy: "creator-id",
	}
	target, err := dataPermissionVersionCreateTarget(policy, datapermission.PolicyScopeTenant)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 6 || fields["id"] != policy.ID || fields["code"] != policy.Code ||
		fields["name"] != policy.Name || fields["scope"] != string(policy.Scope) ||
		fields["tenant_id"] != "tenant-1" || fields["version"] != float64(7) {
		t.Fatalf("unexpected draft target: %s", encoded)
	}
	if _, exists := fields["created_by"]; exists {
		t.Fatalf("draft target leaked actor: %s", encoded)
	}
	policy.TenantID = sql.NullString{}
	if _, err := dataPermissionVersionCreateTarget(policy, datapermission.PolicyScopeTenant); err != datapermission.ErrPolicyNotFound {
		t.Fatalf("missing-tenant target error = %v, want not found", err)
	}
	if _, err := dataPermissionVersionCreateTarget(policy, datapermission.PolicyScopeGlobal); err != datapermission.ErrPolicyNotFound {
		t.Fatalf("wrong-scope target error = %v, want not found", err)
	}
}

func TestDataPermissionHandlerBindRejectsUnknownFields(t *testing.T) {
	const valid = `{"policy":{"api_version":"data-permission.platform/v1","kind":"DataPermissionPolicy","metadata":{"code":"member-owner","name":"Member owner"},"scope":{"type":"global"},"spec":{"subject":{"authenticated":true},"resource":"tenant.member","actions":["read"],"condition":"resource.owner_id == subject.id","effect":"allow"}}}`
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "known fields", body: valid, want: true},
		{name: "unknown root field", body: strings.Replace(valid, `"policy":`, `"policies":{},"policy":`, 1)},
		{name: "unknown policy field", body: strings.Replace(valid, `"kind":`, `"kinds":"DataPermissionPolicy","kind":`, 1)},
		{name: "misspelled condition", body: strings.Replace(valid, `"condition":`, `"conditions":`, 1)},
		{name: "unsupported JSON role alias", body: strings.Replace(valid, `"authenticated":true`, `"role":"manager","authenticated":true`, 1)},
		{name: "unknown subject role field", body: strings.Replace(valid, `"authenticated":true`, `"roles":{"anyOf":["manager"]},"authenticated":true`, 1)},
		{name: "multiple JSON values", body: valid + `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(writer)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/data-permissions/global-policies/create", strings.NewReader(test.body))
			handler := &DataPermissionHandler{logger: slog.Default()}
			var request DataPermissionPolicyCreateRequest
			if got := handler.bind(ctx, &request); got != test.want {
				t.Fatalf("bind() = %v, want %v; response = %s", got, test.want, writer.Body.String())
			}
			if !test.want && writer.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", writer.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestDataPermissionHandlerBindPreservesSimulationAttributes(t *testing.T) {
	const body = `{"policy":{"api_version":"data-permission.platform/v1","kind":"DataPermissionPolicy","metadata":{"code":"member-owner","name":"Member owner"},"scope":{"type":"global"},"spec":{"subject":{"authenticated":true},"resource":"tenant.member","actions":["read"],"effect":"allow"}},"action":"read","subject":{"id":"user-1","type":"user","authenticated":true},"resource":{"type":"tenant.member"},"subject_attributes":{"custom_subject_field":"value"},"resource_attributes":{"custom_resource_field":"value"}}`
	writer := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(writer)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/data-permissions/global-policies/simulate", strings.NewReader(body))
	handler := &DataPermissionHandler{logger: slog.Default()}
	var request DataPermissionSimulationRequest
	if !handler.bind(ctx, &request) {
		t.Fatalf("bind() rejected valid extension attributes: %s", writer.Body.String())
	}
	if request.SubjectAttributes["custom_subject_field"] != "value" || request.ResourceAttributes["custom_resource_field"] != "value" {
		t.Fatalf("simulation attributes lost: subject=%v resource=%v", request.SubjectAttributes, request.ResourceAttributes)
	}
}
