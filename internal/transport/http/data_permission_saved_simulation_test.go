package httptransport

import (
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
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type simulationRecorder struct {
	entries []operationlog.Entry
	err     error
}

func (r *simulationRecorder) Enabled() bool { return true }

func (r *simulationRecorder) Record(_ context.Context, entry operationlog.Entry) error {
	r.entries = append(r.entries, entry)
	return r.err
}

func TestSavedDataPermissionSimulationUsesStoredVersionAndRouteScope(t *testing.T) {
	const policyID = "00000000-0000-0000-0000-000000000001"
	for _, test := range []struct {
		name           string
		path           string
		policyScope    datapermission.PolicyScopeType
		action         string
		resourceTenant string
		version        bool
		logError       bool
		wantStatus     int
	}{
		{name: "saved tenant draft", policyScope: datapermission.PolicyScopeTenant, version: true, wantStatus: http.StatusOK},
		{name: "saved global draft", path: "/api/v1/data-permissions/global-policies/versions/simulate", policyScope: datapermission.PolicyScopeGlobal, version: true, wantStatus: http.StatusOK},
		{name: "global policy on tenant route", policyScope: datapermission.PolicyScopeGlobal, wantStatus: http.StatusNotFound},
		{name: "missing saved version", policyScope: datapermission.PolicyScopeTenant, wantStatus: http.StatusNotFound},
		{name: "foreign tenant resource", policyScope: datapermission.PolicyScopeTenant, resourceTenant: "tenant-2", version: true, wantStatus: http.StatusForbidden},
		{name: "untrusted action is not logged", policyScope: datapermission.PolicyScopeTenant, action: "sensitive-bearer-like-value", version: true, wantStatus: http.StatusBadRequest},
		{name: "failed simulation keeps its error when diagnostic enqueue fails", policyScope: datapermission.PolicyScopeTenant, resourceTenant: "tenant-2", version: true, logError: true, wantStatus: http.StatusForbidden},
		{name: "diagnostic enqueue failure", policyScope: datapermission.PolicyScopeTenant, version: true, logError: true, wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			service := datapermission.NewLifecycleService(datapermission.NewRepository(db), nil, nil, nil, nil, nil, nil, nil)
			schema, err := datapermission.NewSchema("tenant.member", map[string]datapermission.Field{"owner_id": {Column: "tm.user_id", Type: datapermission.ValueTypeText}})
			require.NoError(t, err)
			schemas, err := datapermission.NewSchemaRegistry(schema)
			require.NoError(t, err)
			resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
			require.NoError(t, err)
			engine, err := datapermission.NewRuntimeEngine(schemas, resources)
			require.NoError(t, err)
			logs := &simulationRecorder{}
			if test.logError {
				logs.err = errors.New("outbox unavailable")
			}
			handler := NewDataPermissionHandler(service, datapermission.NewSimulator(schemas, resources, engine), logs, slog.New(slog.NewTextHandler(io.Discard, nil)))
			now := time.Now()
			var tenantID any = "tenant-1"
			if test.policyScope == datapermission.PolicyScopeGlobal {
				tenantID = nil
			}
			mock.ExpectQuery(`SELECT id,code,name,scope,tenant_id`).WithArgs(policyID, "tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "scope", "tenant_id", "published_version_number", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
					AddRow(policyID, "member-own", "Member own", test.policyScope, tenantID, nil, "active", now, "admin-1", now, "admin-1", 1))
			if test.policyScope == datapermission.PolicyScopeTenant || test.version {
				query := mock.ExpectQuery(`SELECT v.id,v.policy_id,v.version_number`).WithArgs(policyID, int64(2), "tenant-1")
				rows := sqlmock.NewRows([]string{"id", "policy_id", "version_number", "document", "status", "published_at", "published_by", "created_at", "created_by", "updated_at", "updated_by", "version"})
				if test.version {
					boundary := datapermission.PolicyBoundary{Type: test.policyScope}
					if test.policyScope == datapermission.PolicyScopeTenant {
						boundary.TenantID = "tenant-1"
					}
					policy := datapermission.Policy{APIVersion: datapermission.PolicyAPIVersion, Kind: datapermission.PolicyKind,
						Metadata: datapermission.PolicyMetadata{Code: "member-own", Name: "Member own"},
						Scope:    boundary,
						Spec:     datapermission.PolicySpec{Resource: "tenant.member", Actions: []string{"update"}, Condition: "resource.owner_id == subject.id", Effect: datapermission.EffectAllow}}
					document, marshalErr := datapermission.MarshalPolicy(policy)
					require.NoError(t, marshalErr)
					rows.AddRow("version-2", policyID, 2, string(document), "draft", nil, nil, now, "admin-1", now, "admin-1", 1)
				}
				query.WillReturnRows(rows)
			}
			path := test.path
			if path == "" {
				path = "/api/v1/data-permissions/tenant-policies/versions/simulate"
			}
			router := gin.New()
			router.POST(path, handler.SimulateVersion)
			resourceTenant := test.resourceTenant
			if resourceTenant == "" {
				resourceTenant = "tenant-1"
			}
			action := test.action
			if action == "" {
				action = "update"
			}
			body := `{"policy_id":"` + policyID + `","version_number":2,"action":"` + action + `","subject":{"id":"user-1","type":"user","authenticated":true,"tenant_id":"tenant-1"},"resource":{"type":"tenant.member","tenant_id":"` + resourceTenant + `"},"subject_attributes":{"id":"user-1"},"resource_attributes":{"owner_id":"user-1"}}`
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request = request.WithContext(platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"}))
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			require.Equal(t, test.wantStatus, recorder.Code, recorder.Body.String())
			require.Len(t, logs.entries, 1)
			require.Equal(t, "data-permission.policy.simulate-version", logs.entries[0].Operation)
			require.Equal(t, policyID, logs.entries[0].ResourceID)
			require.Equal(t, test.wantStatus == http.StatusOK || (test.logError && test.resourceTenant == ""), logs.entries[0].Succeeded)
			require.Equal(t, map[string]any{"scope": expectedScope(test.path), "version_number": int64(2)}, logs.entries[0].Request)
			encodedEntry, err := json.Marshal(logs.entries[0])
			require.NoError(t, err)
			require.NotContains(t, string(encodedEntry), "sensitive-bearer-like-value")
			if test.wantStatus == http.StatusOK {
				var response struct {
					Body datapermission.SimulationResult `json:"body"`
				}
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
				require.False(t, response.Body.Baseline.CurrentAllowed)
				require.True(t, response.Body.Candidate.CurrentAllowed)
				require.True(t, response.Body.Changed)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func expectedScope(path string) datapermission.PolicyScopeType {
	if strings.Contains(path, "/global-policies/") {
		return datapermission.PolicyScopeGlobal
	}
	return datapermission.PolicyScopeTenant
}

func TestSavedDataPermissionSimulationRejectsClientPolicyDocument(t *testing.T) {
	t.Parallel()
	path := "/api/v1/data-permissions/tenant-policies/versions/simulate"
	router := gin.New()
	router.POST(path, (&DataPermissionHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).SimulateVersion)
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"policy_id":"00000000-0000-0000-0000-000000000001","version_number":1,"action":"update","subject":{"id":"user-1","type":"user","authenticated":true,"tenant_id":"tenant-1"},"resource":{"type":"tenant.member","tenant_id":"tenant-1"},"subject_attributes":{"id":"user-1"},"resource_attributes":{"owner_id":"user-1"},"policy":{}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestUnpersistedDataPermissionSimulationRecordsSafeDiagnostic(t *testing.T) {
	t.Parallel()
	schema, err := datapermission.NewSchema("tenant.member", map[string]datapermission.Field{"owner_id": {Column: "tm.user_id", Type: datapermission.ValueTypeText}})
	require.NoError(t, err)
	schemas, err := datapermission.NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	engine, err := datapermission.NewRuntimeEngine(schemas, resources)
	require.NoError(t, err)
	logs := &simulationRecorder{}
	handler := NewDataPermissionHandler(nil, datapermission.NewSimulator(schemas, resources, engine), logs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	path := "/api/v1/data-permissions/tenant-policies/simulate"
	router := gin.New()
	router.POST(path, handler.Simulate)
	policy := datapermission.Policy{
		APIVersion: datapermission.PolicyAPIVersion, Kind: datapermission.PolicyKind,
		Metadata: datapermission.PolicyMetadata{Code: "member-own", Name: "Member own"},
		Scope:    datapermission.PolicyBoundary{Type: datapermission.PolicyScopeTenant, TenantID: "tenant-1"},
		Spec:     datapermission.PolicySpec{Resource: "tenant.member", Actions: []string{"update"}, Condition: "resource.owner_id == subject.id", Effect: datapermission.EffectAllow},
	}
	for _, test := range []struct {
		name           string
		resourceTenant string
		wantStatus     int
	}{
		{name: "allowed", resourceTenant: "tenant-1", wantStatus: http.StatusOK},
		{name: "cross tenant denied", resourceTenant: "tenant-2", wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(DataPermissionSimulationRequest{
				Policy: policy, Action: "update",
				Subject:            resourceaction.Subject{ID: "user-1", Type: "user", Authenticated: true, TenantID: "tenant-1"},
				Resource:           resourceaction.Resource{Type: "tenant.member", TenantID: test.resourceTenant},
				SubjectAttributes:  datapermission.SubjectAttributes{"id": "user-1"},
				ResourceAttributes: datapermission.ResourceAttributes{"owner_id": "user-1"},
			})
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			request = request.WithContext(platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"}))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, test.wantStatus, response.Code, response.Body.String())
			entry := logs.entries[len(logs.entries)-1]
			require.Equal(t, "data-permission.policy.simulate", entry.Operation)
			require.Equal(t, test.wantStatus == http.StatusOK, entry.Succeeded)
			require.Equal(t, map[string]any{"scope": datapermission.PolicyScopeTenant}, entry.Request)
			encoded, err := json.Marshal(entry)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "owner_id")
			require.NotContains(t, string(encoded), "user-1")
		})
	}
}
