//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	userauthentication "github.com/lihongjie0209/go-api-template/internal/authentication"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	appdb "github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/datalifecycle"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/dictionary"
	"github.com/lihongjie0209/go-api-template/internal/files"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/menu"
	"github.com/lihongjie0209/go-api-template/internal/migration"
	"github.com/lihongjie0209/go-api-template/internal/objectstorage"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/lihongjie0209/go-api-template/internal/permission"
	"github.com/lihongjie0209/go-api-template/internal/platformconfig"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
	"github.com/lihongjie0209/go-api-template/internal/tenant"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

type integrationLifecycleLocker struct{}

func (integrationLifecycleLocker) TryLock(context.Context, string, time.Duration) (cache.Lock, bool, error) {
	return integrationLifecycleLock{}, true, nil
}
func (integrationLifecycleLocker) Lock(context.Context, string, time.Duration, time.Duration) (cache.Lock, error) {
	return integrationLifecycleLock{}, nil
}

type integrationLifecycleLock struct{}

func (integrationLifecycleLock) Extend(context.Context) error { return nil }
func (integrationLifecycleLock) Unlock(context.Context) error { return nil }
func (integrationLifecycleLock) Until() time.Time             { return time.Now().Add(time.Minute) }

func TestRepositoryAndMigrations(t *testing.T) {
	for _, databaseType := range []string{"postgres", "mysql"} {
		t.Run(databaseType, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			dsn, migrationURL := startDatabase(t, ctx, databaseType)
			migrationPath, err := filepath.Abs(filepath.Join("..", "migrations", databaseType))
			if err != nil {
				t.Fatal(err)
			}
			schema := ""
			if databaseType == "postgres" {
				schema = "integration_postgres"
			}
			migrationCfg := config.Migration{Path: migrationPath, DatabaseURL: migrationURL, Table: "integration_" + databaseType + "_schema_migrations", Schema: schema, CreateSchema: schema != ""}
			migrationErrors := make(chan error, 3)
			var migrations sync.WaitGroup
			for range 3 {
				migrations.Add(1)
				go func() {
					defer migrations.Done()
					migrationErrors <- migration.Run(migrationCfg, "up", 0)
				}()
			}
			migrations.Wait()
			close(migrationErrors)
			for err := range migrationErrors {
				if err != nil {
					t.Fatalf("concurrent migration up: %v", err)
				}
			}

			db, err := appdb.Open(ctx, config.Database{Type: databaseType, DSN: dsn, Schema: schema, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute, PingTimeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			var userTables int
			if databaseType == "postgres" {
				if err := db.GetContext(ctx, &userTables, `SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename = 'users'`); err != nil {
					t.Fatal(err)
				}
				var timezone string
				if err := db.GetContext(ctx, &timezone, `SHOW TIMEZONE`); err != nil || timezone != "Asia/Shanghai" {
					t.Fatalf("timezone=%q err=%v", timezone, err)
				}
			} else if err := db.GetContext(ctx, &userTables, `SELECT count(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'users'`); err != nil {
				t.Fatal(err)
			} else {
				var timezone string
				if err := db.GetContext(ctx, &timezone, `SELECT @@session.time_zone`); err != nil || timezone != "+08:00" {
					t.Fatalf("timezone=%q err=%v", timezone, err)
				}
			}
			if userTables != 0 {
				t.Fatal("generic template migration must not create a users table")
			}
			assertServiceMigrationHistory(t, ctx, db, databaseType, migrationCfg.Table)
			if databaseType == "postgres" {
				testPostgresAuditInfrastructure(t, ctx, db)
				testPostgresLogPartitions(t, ctx, db)
			} else {
				testMySQLAuditInfrastructure(t, ctx, db)
				testMySQLLogRetention(t, ctx, db)
			}
			testTenantLifecycle(t, ctx, db)
			testIdentityUserLifecycle(t, ctx, db)
			testServiceAccountLifecycle(t, ctx, db)
			testLogQueryPresentation(t, ctx, db)
			permissionID := testPermissionLifecycle(t, ctx, db)
			testTenantAuthorizationLifecycle(t, ctx, db, permissionID)
			testMenuLifecycle(t, ctx, db, permissionID)
			testPlatformConfigLifecycle(t, ctx, db)
			testDictionaryLifecycle(t, ctx, db)
			testPBACLifecycle(t, ctx, db)
			testDataPermissionLifecycle(t, ctx, db)
			assertFileQueryIndexes(t, ctx, db, databaseType)
			testFileLifecycle(t, ctx, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := migration.Run(migrationCfg, "down", 0); err != nil {
				t.Fatalf("migration down: %v", err)
			}
		})
	}
}

func testDictionaryLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "dictionary-test", Type: platformprincipal.TypeUser})
	service := dictionary.New(db, appdb.NewTransactor(db), nil, dictionary.NewProviderRegistry(), nil, nil, config.Config{Dictionary: config.Dictionary{CacheTTL: time.Minute, MaxTreeNodes: 100}})
	definition, err := service.CreateDefinition(actorCtx, dictionary.DefinitionInput{Code: "test.region", Name: "Regions", Kind: dictionary.KindTree, Source: dictionary.SourceStatic, Status: "active"})
	if err != nil {
		t.Fatalf("create dictionary: %v", err)
	}
	root, err := service.CreateItem(actorCtx, dictionary.ItemInput{DictionaryID: definition.ID, Code: "cn", Name: "China", SortOrder: 1})
	if err != nil {
		t.Fatalf("create root dictionary item: %v", err)
	}
	child, err := service.CreateItem(actorCtx, dictionary.ItemInput{DictionaryID: definition.ID, ParentID: &root.ID, Code: "gd", Name: "Guangdong", SortOrder: 1})
	if err != nil {
		t.Fatalf("create child dictionary item: %v", err)
	}
	result, err := service.Query(ctx, dictionary.Query{Code: "test.region"})
	if err != nil {
		t.Fatalf("query dictionary: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Children) != 1 || result.Items[0].Children[0].Code != "gd" {
		t.Fatalf("unexpected dictionary tree: %#v", result.Items)
	}
	filtered, err := service.Query(ctx, dictionary.Query{Code: "test.region", Keyword: "guang"})
	if err != nil || len(filtered.Items) != 1 || len(filtered.Items[0].Children) != 1 {
		t.Fatalf("tree search must retain ancestors: %#v err=%v", filtered.Items, err)
	}
	if _, err := service.UpdateItem(actorCtx, dictionary.ItemUpdate{ID: child.ID, ParentID: &root.ID, Name: "Guangdong", Value: "", SortOrder: 1, Version: child.Version + 1}); !errors.Is(err, dictionary.ErrConflict) {
		t.Fatalf("stale dictionary item update error=%v", err)
	}
	stored, err := service.GetItem(actorCtx, root.ID)
	if err != nil || stored.CreatedBy != "dictionary-test" || stored.Version != 1 {
		t.Fatalf("dictionary audit fields not maintained: %#v err=%v", stored, err)
	}
	enumDefinition, err := service.CreateDefinition(actorCtx, dictionary.DefinitionInput{Code: "test.gender", Name: "Gender", Kind: dictionary.KindEnum, Source: dictionary.SourceStatic, Status: "active"})
	if err != nil {
		t.Fatalf("create enum dictionary: %v", err)
	}
	for index, code := range []string{"m", "f", "u"} {
		if _, err := service.CreateItem(actorCtx, dictionary.ItemInput{DictionaryID: enumDefinition.ID, Code: code, Name: strings.ToUpper(code), SortOrder: int64(index)}); err != nil {
			t.Fatalf("create enum item: %v", err)
		}
	}
	enumPage, err := service.Query(ctx, dictionary.Query{Code: "test.gender", Page: 2, PageSize: 2})
	if err != nil || enumPage.Total != 3 || len(enumPage.Items) != 1 || enumPage.Page != 2 {
		t.Fatalf("unexpected enum page: %#v err=%v", enumPage, err)
	}
}

func testPBACLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	registry, err := pbac.NewRegistry([]pbac.ResourceDefinition{{
		Key: "integration.member", Name: "Integration member", Scope: pbac.ResourceScopeTenant,
		Actions: []pbac.ActionDefinition{{Key: "update", Name: "Update"}},
	}})
	if err != nil {
		t.Fatalf("create pbac registry: %v", err)
	}
	service := pbac.NewLifecycleService(
		pbac.NewRepository(db), appdb.NewTransactor(db), registry, nil, nil, nil,
	)
	actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{
		ID: "pbac-admin", Type: platformprincipal.TypeUser, TenantID: "tenant-a",
	})
	authenticated := true
	document := pbac.Policy{
		APIVersion: pbac.APIVersionV1,
		Kind:       pbac.KindPolicy,
		Metadata: pbac.PolicyMetadata{
			Code: "integration-member-update", Name: "Integration member update",
		},
		Scope: pbac.PolicyScope{Type: pbac.PolicyScopeTenant, TenantID: "tenant-a"},
		Spec: pbac.PolicySpec{
			Subject:  pbac.SubjectMatcher{Authenticated: &authenticated},
			Resource: pbac.ResourceMatcher{Type: "integration.member"},
			Actions:  []string{"update"},
			Effect:   pbac.EffectAllow,
		},
	}
	created, err := service.Create(actorCtx, pbac.CreatePolicyInput{Document: document})
	if err != nil {
		t.Fatalf("create pbac policy: %v", err)
	}
	if created.Policy.Version != 1 || created.Version.Status != pbac.VersionStatusDraft {
		t.Fatalf("created pbac policy=%+v version=%+v", created.Policy, created.Version)
	}
	published, err := service.Publish(actorCtx, pbac.PublishInput{
		PolicyID: created.Policy.ID, VersionNumber: 1, ExpectedPolicyVersion: created.Policy.Version,
	})
	if err != nil {
		t.Fatalf("publish pbac policy: %v", err)
	}
	if published.Policy.PublishedVersionNumber == nil || *published.Policy.PublishedVersionNumber != 1 ||
		published.Version.Status != pbac.VersionStatusPublished {
		t.Fatalf("published pbac policy=%+v version=%+v", published.Policy, published.Version)
	}
	engine, err := pbac.NewEngine(registry, nil)
	if err != nil {
		t.Fatalf("create pbac engine: %v", err)
	}
	if err := pbac.NewRuntimeLoader(pbac.NewRepository(db), engine).Refresh(ctx); err != nil {
		t.Fatalf("refresh pbac runtime: %v", err)
	}
	decision, err := engine.Evaluate(ctx, pbac.EvaluationRequest{
		Subject:  pbac.Subject{Authenticated: true, TenantID: "tenant-a"},
		Resource: pbac.Resource{Type: "integration.member", TenantID: "tenant-a"},
		Action:   "update",
	})
	if err != nil || decision.Effect != pbac.DecisionEffectAllow {
		t.Fatalf("pbac decision=%+v err=%v", decision, err)
	}
	denied, err := engine.Evaluate(ctx, pbac.EvaluationRequest{
		Subject:  pbac.Subject{Authenticated: true, TenantID: "tenant-b"},
		Resource: pbac.Resource{Type: "integration.member", TenantID: "tenant-a"},
		Action:   "update",
	})
	if err != nil || denied.Effect != pbac.DecisionEffectDeny || denied.ReasonCode != pbac.ReasonTenantMismatch {
		t.Fatalf("cross-tenant pbac decision=%+v err=%v", denied, err)
	}
}

func testDataPermissionLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	schema, err := datapermission.NewSchema("tenant.member", map[string]datapermission.Field{
		"owner_id": {Column: "tm.user_id", Type: datapermission.ValueTypeText},
	})
	if err != nil {
		t.Fatalf("create data permission schema: %v", err)
	}
	schemas, err := datapermission.NewSchemaRegistry(schema)
	if err != nil {
		t.Fatalf("create data permission schema registry: %v", err)
	}
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	if err != nil {
		t.Fatalf("create data permission resource registry: %v", err)
	}
	engine, err := datapermission.NewRuntimeEngine(schemas, resources)
	if err != nil {
		t.Fatalf("create data permission engine: %v", err)
	}
	repository := datapermission.NewRepository(db)
	service := datapermission.NewLifecycleService(repository, appdb.NewTransactor(db), schemas, resources, datapermission.NewRuntimeLoader(repository, engine), nil, nil)
	authenticated := true
	document := datapermission.Policy{
		APIVersion: datapermission.PolicyAPIVersion,
		Kind:       datapermission.PolicyKind,
		Metadata:   datapermission.PolicyMetadata{Code: "integration-member-owner", Name: "Integration member owner"},
		Scope:      datapermission.PolicyBoundary{Type: datapermission.PolicyScopeTenant, TenantID: "tenant-data"},
		Spec: datapermission.PolicySpec{
			Subject:   pbac.SubjectMatcher{Authenticated: &authenticated},
			Resource:  "tenant.member",
			Actions:   []string{"read", "list"},
			Condition: "resource.owner_id == subject.id",
			Effect:    datapermission.EffectAllow,
		},
	}
	actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "data-admin", Type: platformprincipal.TypeUser, TenantID: "tenant-data"})
	created, version, err := service.Create(actorCtx, document)
	if err != nil || created.Version != 1 || version.Status != datapermission.VersionDraft {
		t.Fatalf("create data permission policy=%+v version=%+v err=%v", created, version, err)
	}
	published, err := service.Publish(actorCtx, created.ID, 1, created.Version)
	if err != nil || !published.PublishedVersionNumber.Valid || published.PublishedVersionNumber.Int64 != 1 {
		t.Fatalf("publish data permission policy=%+v err=%v", published, err)
	}
	predicate, err := engine.CompileSQL(ctx, "tenant.member", "read", pbac.Subject{ID: "member-1", Type: "user", Authenticated: true, TenantID: "tenant-data"}, datapermission.SubjectAttributes{"id": "member-1"})
	if err != nil || predicate.Clause != "(tm.user_id = ?)" || len(predicate.Args) != 1 || predicate.Args[0] != "member-1" {
		t.Fatalf("compiled data permission predicate=%+v err=%v", predicate, err)
	}
	disabled, err := service.SetStatus(actorCtx, datapermission.SetPolicyStatusInput{PolicyID: created.ID, Status: datapermission.StatusDisabled, ExpectedPolicyVersion: published.Version})
	if err != nil || disabled.Status != datapermission.StatusDisabled {
		t.Fatalf("disable data permission policy=%+v err=%v", disabled, err)
	}
	denied, err := engine.CompileSQL(ctx, "tenant.member", "read", pbac.Subject{ID: "member-1", Type: "user", Authenticated: true, TenantID: "tenant-data"}, datapermission.SubjectAttributes{"id": "member-1"})
	if err != nil || denied.Clause != "(1 = 0)" {
		t.Fatalf("disabled data permission predicate=%+v err=%v", denied, err)
	}
}

func testLogQueryPresentation(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	const (
		actorID  = "log-presentation-user"
		tenantID = "log-presentation-tenant"
	)
	now := time.Now().UTC().Truncate(time.Microsecond)
	actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: actorID, Type: platformprincipal.TypeUser, TenantID: tenantID})
	err := appdb.NewTransactor(db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO identity_users(id,username,display_name,email,phone,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,1)`), actorID, "log.presentation.user", "Log Presentation User", "", "", "active", now, actorID, now, actorID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO operation_logs(id,tenant_id,actor_id,actor_name_snapshot,actor_type,application_id,source,operation,resource_type,resource_id,resource_name_snapshot,protocol,method,route,request_payload,duration_ms,succeeded,error_code,error_message,request_id,trace_id,client_ip,user_agent,extension,occurred_at,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,CAST(? AS JSON),?,?,?,?,?,1)`), "operation-presentation", tenantID, actorID, "Historical Actor", "user", "", "backend", "presentation.test", "test", "resource-1", "Historical Resource", "service", "", "", "", 1, true, "", "", "", "", "", "", `{}`, now, now, actorID, now, actorID); err != nil {
			return err
		}
		_, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO security_logs(id,tenant_id,actor_id,actor_name_snapshot,actor_type,subject_id,subject_name_snapshot,subject_type,event_type,succeeded,reason,error_code,error_message,identifier_hash,token_id_hash,session_id,request_id,trace_id,client_ip,user_agent,metadata,occurred_at,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,CAST(? AS JSON),?,?,?,?,?,1)`), "security-presentation", tenantID, actorID, "Historical Actor", "user", actorID, "Historical Subject", "user", "login", true, "", "", "", "", "", "", "", "", "", "", `{}`, now, now, actorID, now, actorID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	actors := staticActorResolver{actorID: "Log Presentation User"}
	operationService := operationlog.New(config.Config{}, nil, db, nil, nil, nil, actors)
	operationRecord, err := operationService.Get(actorCtx, "operation-presentation")
	if err != nil {
		t.Fatalf("get operation presentation: %v", err)
	}
	securityService := securitylog.New(config.Config{}, nil, db, nil, nil, nil, actors)
	securityRecord, err := securityService.Get(actorCtx, "security-presentation")
	if err != nil {
		t.Fatalf("get security presentation: %v", err)
	}
	for name, value := range map[string]struct {
		actorName, relatedName, createdByName, updatedByName string
		occurredAt                                           time.Time
	}{
		"operation": {operationRecord.ActorName, operationRecord.ResourceName, operationRecord.CreatedByName, operationRecord.UpdatedByName, operationRecord.OccurredAt},
		"security":  {securityRecord.ActorName, securityRecord.SubjectName, securityRecord.CreatedByName, securityRecord.UpdatedByName, securityRecord.OccurredAt},
	} {
		if value.actorName != "Historical Actor" || !strings.HasPrefix(value.relatedName, "Historical ") || value.createdByName != "Log Presentation User" || value.updatedByName != "Log Presentation User" {
			t.Fatalf("%s display names = %+v", name, value)
		}
		_, offset := value.occurredAt.Zone()
		if offset != 8*60*60 || !value.occurredAt.Equal(now) {
			t.Fatalf("%s occurred_at = %v", name, value.occurredAt)
		}
	}
}

func assertFileQueryIndexes(t *testing.T, ctx context.Context, db *sqlx.DB, databaseType string) {
	t.Helper()
	wanted := map[string]string{
		"files_object_delete_pending_idx":  "files",
		"files_tenant_content_created_idx": "files",
		"files_tenant_creator_created_idx": "files",
		"files_tenant_size_created_idx":    "files",
		"file_upload_intents_pending_idx":  "file_upload_intents",
	}
	for index, table := range wanted {
		var count int
		if databaseType == "postgres" {
			if err := db.GetContext(ctx, &count, `SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND tablename=$1 AND indexname=$2`, table, index); err != nil {
				t.Fatal(err)
			}
		} else if err := db.GetContext(ctx, &count, `SELECT count(DISTINCT index_name) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=?`, table, index); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("file index %s count=%d", index, count)
		}
	}
}

func testMySQLLogRetention(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	old := time.Now().AddDate(-3, 0, 0)
	now := time.Now()
	operationInsert := db.Rebind(`INSERT INTO operation_logs(id,tenant_id,actor_id,actor_type,application_id,source,operation,resource_type,resource_id,protocol,method,route,request_payload,duration_ms,succeeded,error_code,error_message,request_id,trace_id,client_ip,user_agent,extension,occurred_at,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	securityInsert := db.Rebind(`INSERT INTO security_logs(id,tenant_id,actor_id,actor_type,subject_id,subject_type,event_type,succeeded,reason,error_code,error_message,identifier_hash,token_id_hash,session_id,request_id,trace_id,client_ip,user_agent,metadata,occurred_at,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	actorCtx := platformprincipal.SystemContext(ctx, "retention-test")
	if err := appdb.NewTransactor(db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(actorCtx, operationInsert, "expired-operation", "tenant-retention", "actor", "system", "", "backend", "retention.test", "", "", "service", "", "", "", 1, true, "", "", "", "", "", "", `{}`, old, now, "ignored", now, "ignored", 99); err != nil {
			return err
		}
		_, err := tx.ExecContext(actorCtx, securityInsert, "expired-security", "tenant-retention", "actor", "system", "subject", "user", "login", true, "", "", "", "", "", "", "", "", "", "", `{}`, old, now, "ignored", now, "ignored", 99)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	manager := datalifecycle.New(db, config.Config{App: config.App{Name: "integration"}, Database: config.Database{Type: "mysql"}, DistributedLock: config.DistributedLock{TTL: time.Second}, DataLifecycle: config.DataLifecycle{
		Enabled: true, Interval: time.Hour, Timeout: time.Minute, PremakeMonths: 6, PurgeBatchSize: 100, PurgeMaxBatches: 10,
		OperationLogRetentionMonths: 12, SecurityLogRetentionMonths: 24,
	}}, slog.Default(), nil, integrationLifecycleLocker{})
	if err := manager.Maintain(ctx); err != nil {
		t.Fatalf("maintain mysql log retention: %v", err)
	}
	for _, record := range []struct{ table, id string }{{"operation_logs", "expired-operation"}, {"security_logs", "expired-security"}} {
		var count int
		if err := db.GetContext(ctx, &count, db.Rebind(`SELECT count(*) FROM `+record.table+` WHERE id=?`), record.id); err != nil || count != 0 {
			t.Fatalf("expired %s count=%d err=%v", record.table, count, err)
		}
	}
}

func testPostgresLogPartitions(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	for _, table := range []string{"operation_logs", "security_logs"} {
		var partitioned bool
		query := `SELECT EXISTS (
			SELECT 1 FROM pg_partitioned_table p
			JOIN pg_class c ON c.oid=p.partrelid
			JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=current_schema() AND c.relname=$1
		)`
		if err := db.GetContext(ctx, &partitioned, query, table); err != nil || !partitioned {
			t.Fatalf("table %s partitioned=%v err=%v", table, partitioned, err)
		}
	}

	actorCtx := platformprincipal.SystemContext(ctx, "log-partition-integration")
	occurredAt := time.Now().UTC().Truncate(time.Microsecond)
	err := appdb.NewTransactor(db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		operationInsert := `INSERT INTO operation_logs(id,tenant_id,actor_id,actor_type,source,operation,protocol,duration_ms,succeeded,occurred_at,created_at,created_by,updated_at,updated_by,version)
			VALUES($1,'tenant-partition','actor','system','backend','partition.test','service',1,true,$2,now(),'ignored',now(),'ignored',99)
			ON CONFLICT(id,occurred_at) DO NOTHING`
		if _, execErr := tx.ExecContext(actorCtx, operationInsert, "partition-operation", occurredAt); execErr != nil {
			return execErr
		}
		if _, execErr := tx.ExecContext(actorCtx, operationInsert, "partition-operation", occurredAt); execErr != nil {
			return execErr
		}
		securityInsert := `INSERT INTO security_logs(id,event_type,succeeded,occurred_at,created_at,created_by,updated_at,updated_by,version)
			VALUES($1,'login',true,$2,now(),'ignored',now(),'ignored',99)
			ON CONFLICT(id,occurred_at) DO NOTHING`
		if _, execErr := tx.ExecContext(actorCtx, securityInsert, "partition-security", occurredAt); execErr != nil {
			return execErr
		}
		_, execErr := tx.ExecContext(actorCtx, securityInsert, "partition-security", occurredAt)
		return execErr
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []struct{ table, id string }{{"operation_logs", "partition-operation"}, {"security_logs", "partition-security"}} {
		var partition string
		query := fmt.Sprintf(`SELECT tableoid::regclass::text FROM %s WHERE id=$1 AND occurred_at=$2`, record.table)
		if err := db.GetContext(ctx, &partition, query, record.id, occurredAt); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(partition, "_default") {
			t.Fatalf("current %s row routed to default partition %q", record.table, partition)
		}
		var count int
		if err := db.GetContext(ctx, &count, fmt.Sprintf(`SELECT count(*) FROM %s WHERE id=$1 AND occurred_at=$2`, record.table), record.id, occurredAt); err != nil || count != 1 {
			t.Fatalf("%s duplicate count=%d err=%v", record.table, count, err)
		}
	}
	manager := datalifecycle.New(db, config.Config{App: config.App{Name: "integration"}, Database: config.Database{Type: "postgres", Name: "app", Schema: "public"}, DistributedLock: config.DistributedLock{TTL: time.Second}, DataLifecycle: config.DataLifecycle{
		Enabled: true, Interval: time.Hour, Timeout: time.Minute, PremakeMonths: 7, PurgeMaxBatches: 10,
		OperationLogRetentionMonths: 120, SecurityLogRetentionMonths: 120,
	}}, slog.Default(), nil, integrationLifecycleLocker{})
	if err := manager.Maintain(ctx); err != nil {
		t.Fatalf("maintain log partitions: %v", err)
	}
	future := time.Now().UTC().AddDate(0, 7, 0)
	partition := fmt.Sprintf("operation_logs_y%04dm%02d", future.Year(), int(future.Month()))
	var exists bool
	if err := db.GetContext(ctx, &exists, `SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`, partition); err != nil || !exists {
		t.Fatalf("future partition %s exists=%v err=%v", partition, exists, err)
	}
}

func testTenantAuthorizationLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB, permissionID string) {
	t.Helper()
	const (
		tenantID       = "authorization-tenant"
		adminMemberID  = "authorization-admin-member"
		targetMemberID = "authorization-target-member"
	)
	actorCtx := platformprincipal.SystemContext(ctx, "authorization-integration")
	now := time.Now()
	err := appdb.NewTransactor(db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		statements := []struct {
			query string
			args  []any
		}{
			{
				`INSERT INTO tenants(id,code,name,description,status,owner_user_id,owner_name,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`,
				[]any{tenantID, "authorization-integration", "Authorization Integration", "", "active", "authorization-admin", "Authorization Admin", now, "authorization-integration", now, "authorization-integration"},
			},
			{
				`INSERT INTO tenant_memberships(id,tenant_id,user_id,username,display_name,status,joined_at,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`,
				[]any{adminMemberID, tenantID, "authorization-admin", "authorization.admin", "Authorization Admin", "active", now, now, "authorization-integration", now, "authorization-integration"},
			},
			{
				`INSERT INTO tenant_memberships(id,tenant_id,user_id,username,display_name,status,joined_at,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`,
				[]any{targetMemberID, tenantID, "authorization-target", "authorization.target", "Authorization Target", "active", now, now, "authorization-integration", now, "authorization-integration"},
			},
		}
		for _, statement := range statements {
			if _, execErr := tx.ExecContext(actorCtx, tx.Rebind(statement.query), statement.args...); execErr != nil {
				return execErr
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{DistributedLock: config.DistributedLock{TTL: time.Second, RetryDelay: 10 * time.Millisecond}}
	definitions := pbac.PlatformResourceDefinitions()
	definitions = append(definitions, pbac.ResourceDefinition{Key: "integration.permissions", Name: "Integration permissions", Scope: pbac.ResourceScopeTenant, Actions: []pbac.ActionDefinition{{Key: "read", Name: "Read"}}})
	registry, registryErr := pbac.NewRegistryFromDefinitions(definitions)
	if registryErr != nil {
		t.Fatal(registryErr)
	}
	service := authorization.NewTenantAuthorizationService(db, appdb.NewTransactor(db), nil, discardOperationRecorder{}, discardSecurityRecorder{}, nil, cfg, nil, registry)
	if err := service.SetTenantPermissions(actorCtx, tenantID, 1, []string{permissionID}); err != nil {
		t.Fatalf("set tenant permission ceiling: %v", err)
	}
	if err := service.SetTenantPermissions(actorCtx, tenantID, 1, []string{permissionID}); !errors.Is(err, authorization.ErrTenantAuthorizationConflict) {
		t.Fatalf("stale tenant permission ceiling error=%v", err)
	}
	if err := service.SetAdministrator(actorCtx, tenantID, adminMemberID, true); err != nil {
		t.Fatalf("set tenant administrator: %v", err)
	}
	if err := service.SetAdministrator(actorCtx, tenantID, adminMemberID, false); !errors.Is(err, authorization.ErrTenantAuthorizationConflict) {
		t.Fatalf("remove final tenant administrator error=%v", err)
	}
	// Direct service integration uses an explicit system principal; transport
	// end-to-end tests cover user endpoint descriptors and policy evaluation.
	adminCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "authorization-admin", Type: platformprincipal.TypeSystem, TenantID: tenantID, MembershipID: adminMemberID})
	role, err := service.CreateRole(adminCtx, "auditor", "审计员", "integration role", []string{permissionID})
	if err != nil || role.Version != 1 {
		t.Fatalf("created tenant role=%+v err=%v", role, err)
	}
	permissions, err := service.RolePermissions(adminCtx, role.ID)
	if err != nil || len(permissions) != 1 || permissions[0].ID != permissionID || permissions[0].Name == "" {
		t.Fatalf("role permissions=%+v err=%v", permissions, err)
	}
	if err := service.SetMemberRoles(adminCtx, targetMemberID, 1, []string{role.ID}); err != nil {
		t.Fatalf("set member roles: %v", err)
	}
	if err := service.SetMemberRoles(adminCtx, targetMemberID, 1, []string{role.ID}); !errors.Is(err, authorization.ErrTenantAuthorizationConflict) {
		t.Fatalf("stale member roles error=%v", err)
	}
	memberRoles, err := service.MemberRoles(adminCtx, targetMemberID)
	if err != nil || len(memberRoles) != 1 || memberRoles[0].Name != "审计员" {
		t.Fatalf("member roles=%+v err=%v", memberRoles, err)
	}
	targetCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "authorization-target", Type: platformprincipal.TypeUser, TenantID: tenantID, MembershipID: targetMemberID})
	effective, err := service.EffectivePermissions(targetCtx, "")
	if err != nil || len(effective) != 1 || effective[0] != permissionID {
		t.Fatalf("effective permissions=%v err=%v", effective, err)
	}
	page, err := service.PageRoles(adminCtx, authorization.RolePageInput{Request: pagination.Request{Page: 1, PageSize: 20}, Keyword: "审计", IDs: []string{role.ID}, Statuses: []string{"active"}})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("tenant role page=%+v err=%v", page, err)
	}
	otherTenantCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "other", Type: platformprincipal.TypeSystem, TenantID: "other-tenant", MembershipID: "other-member"})
	if _, err := service.GetRole(otherTenantCtx, role.ID); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("cross-tenant role lookup error=%v", err)
	}
	updated, err := service.UpdateRole(adminCtx, role.ID, "高级审计员", role.Description, "active", role.Version)
	if err != nil || updated.Version != role.Version+1 {
		t.Fatalf("updated tenant role=%+v err=%v", updated, err)
	}
	if _, err := service.UpdateRole(adminCtx, role.ID, "旧版本", role.Description, "active", role.Version); !errors.Is(err, authorization.ErrTenantAuthorizationConflict) {
		t.Fatalf("stale tenant role update error=%v", err)
	}
	if err := service.DeleteRole(adminCtx, role.ID, updated.Version); err != nil {
		t.Fatalf("delete tenant role: %v", err)
	}
	if _, err := service.GetRole(adminCtx, role.ID); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("deleted tenant role lookup error=%v", err)
	}
}

func testServiceAccountLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	service := serviceaccount.New(db, appdb.NewTransactor(db), discardOperationRecorder{}, discardSecurityRecorder{}, config.Config{Authentication: config.Authentication{MaxFailedAttempts: 5, LockDuration: time.Minute}})
	actorCtx := platformprincipal.SystemContext(ctx, "service-account-integration")
	created, err := service.Create(actorCtx, serviceaccount.CreateInput{ClientID: "integration-worker", Name: "Integration Worker", Description: "database compatibility test"})
	if err != nil || created.Secret == "" || created.Account.Version != 1 {
		t.Fatalf("created service account=%+v err=%v", created, err)
	}
	if _, err := service.Authenticate(ctx, "INTEGRATION-WORKER", created.Secret); err != nil {
		t.Fatalf("authenticate created service account: %v", err)
	}
	current, err := service.Get(actorCtx, created.Account.ID)
	if err != nil || current.Version != 2 || current.LastUsedAt == nil {
		t.Fatalf("service account after authentication=%+v err=%v", current, err)
	}
	updated, err := service.Update(actorCtx, serviceaccount.UpdateInput{ID: current.ID, Name: "Updated Worker", Description: current.Description, Status: serviceaccount.StatusActive, Version: current.Version})
	if err != nil || updated.Version != 3 {
		t.Fatalf("updated service account=%+v err=%v", updated, err)
	}
	if _, err := service.Update(actorCtx, serviceaccount.UpdateInput{ID: current.ID, Name: "Stale", Status: serviceaccount.StatusActive, Version: current.Version}); !errors.Is(err, serviceaccount.ErrConflict) {
		t.Fatalf("stale service account update error=%v", err)
	}
	rotated, err := service.RotateSecret(actorCtx, updated.ID, updated.Version)
	if err != nil || rotated.Secret == "" || rotated.Secret == created.Secret || rotated.Account.Version != 4 {
		t.Fatalf("rotated service account=%+v err=%v", rotated, err)
	}
	if _, err := service.Authenticate(ctx, created.Account.ClientID, created.Secret); !errors.Is(err, serviceaccount.ErrInvalidCredentials) {
		t.Fatalf("old service account secret error=%v", err)
	}
	if _, err := service.Authenticate(ctx, created.Account.ClientID, rotated.Secret); err != nil {
		t.Fatalf("rotated service account secret: %v", err)
	}
	current, err = service.Get(actorCtx, created.Account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(actorCtx, current.ID, current.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, created.Account.ClientID, rotated.Secret); !errors.Is(err, serviceaccount.ErrInvalidCredentials) {
		t.Fatalf("deleted service account authentication error=%v", err)
	}
}

func assertServiceMigrationHistory(t *testing.T, ctx context.Context, db *sqlx.DB, databaseType, table string) {
	t.Helper()
	var customHistory, defaultHistory int
	if databaseType == "postgres" {
		if err := db.GetContext(ctx, &customHistory, `SELECT count(*) FROM pg_tables WHERE schemaname=current_schema() AND tablename=$1`, table); err != nil {
			t.Fatal(err)
		}
		if err := db.GetContext(ctx, &defaultHistory, `SELECT count(*) FROM pg_tables WHERE schemaname=current_schema() AND tablename='schema_migrations'`); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := db.GetContext(ctx, &customHistory, `SELECT count(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?`, table); err != nil {
			t.Fatal(err)
		}
		if err := db.GetContext(ctx, &defaultHistory, `SELECT count(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='schema_migrations'`); err != nil {
			t.Fatal(err)
		}
	}
	if customHistory != 1 || defaultHistory != 0 {
		t.Fatalf("migration history custom=%d default=%d", customHistory, defaultHistory)
	}
}

func testIdentityUserLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	service := identity.New(identity.NewRepository(db), appdb.NewTransactor(db), nil, discardOperationRecorder{}, discardSecurityRecorder{}, slog.Default(), config.Config{User: config.User{CacheTTL: time.Minute}})
	actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "platform-admin", Type: platformprincipal.TypeUser})
	created, err := service.Create(actorCtx, identity.CreateInput{Username: "Alice.Smith", DisplayName: "Alice", Email: "alice@example.com", Phone: "13800000000"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Username != "alice.smith" || created.Version != 1 {
		t.Fatalf("created user=%+v", created)
	}
	creatorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: created.ID, Type: platformprincipal.TypeUser})
	presented, err := service.Create(creatorCtx, identity.CreateInput{Username: "bob.smith", DisplayName: "Bob"})
	if err != nil || presented.CreatedByName != "Alice" || presented.UpdatedByName != "Alice" {
		t.Fatalf("presented actor names user=%+v err=%v", presented, err)
	}
	if err := service.Delete(creatorCtx, presented.ID, presented.Version); err != nil {
		t.Fatalf("delete presentation fixture: %v", err)
	}
	if _, err := service.Create(actorCtx, identity.CreateInput{Username: "ALICE.SMITH", DisplayName: "Duplicate"}); !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("duplicate username error=%v", err)
	}
	updated, err := service.Update(actorCtx, identity.UpdateInput{ID: created.ID, DisplayName: "Alice Updated", Email: created.Email, Phone: created.Phone, Status: identity.StatusActive, Version: created.Version})
	if err != nil || updated.Version != 2 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	jwtConfig, keyErr := testutil.JWTConfig()
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	jwtConfig.Issuer = "identity-service"
	authCfg := config.Config{App: config.App{Name: "identity-service"}, JWT: jwtConfig, Authentication: config.Authentication{RefreshTTL: 24 * time.Hour, MaxFailedAttempts: 5, LockDuration: time.Minute}}
	authenticationService := userauthentication.New(db, appdb.NewTransactor(db), service, auth.New(authCfg), authCfg)
	if err := authenticationService.SetPassword(actorCtx, created.ID, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	tokens, err := authenticationService.Login(ctx, "ALICE.SMITH", "correct horse battery staple", "127.0.0.1", "integration-test")
	if err != nil || tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("login tokens=%+v err=%v", tokens, err)
	}
	rotated, err := authenticationService.Refresh(ctx, tokens.RefreshToken)
	if err != nil || rotated.RefreshToken == tokens.RefreshToken {
		t.Fatalf("refresh tokens=%+v err=%v", rotated, err)
	}
	if err := authenticationService.Logout(ctx, rotated.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticationService.Refresh(ctx, tokens.RefreshToken); !errors.Is(err, userauthentication.ErrRefreshReused) {
		t.Fatalf("replayed refresh error=%v", err)
	}
	if err := service.Delete(actorCtx, created.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(actorCtx, identity.CreateInput{Username: "alice.smith", DisplayName: "Reused Identity"}); !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("deleted username reuse error=%v", err)
	}
}

type discardOperationRecorder struct{}

func (discardOperationRecorder) Enabled() bool                                    { return true }
func (discardOperationRecorder) Record(context.Context, operationlog.Entry) error { return nil }
func (discardOperationRecorder) RecordTx(context.Context, *sqlx.Tx, operationlog.Entry) error {
	return nil
}

type discardSecurityRecorder struct{}

func (discardSecurityRecorder) Enabled() bool                                   { return true }
func (discardSecurityRecorder) FailClosed() bool                                { return true }
func (discardSecurityRecorder) Record(context.Context, securitylog.Entry) error { return nil }
func (discardSecurityRecorder) RecordTx(context.Context, *sqlx.Tx, securitylog.Entry) error {
	return nil
}

func testPermissionLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) string {
	t.Helper()
	definitions := pbac.PlatformResourceDefinitions()
	definitions = append(definitions, pbac.ResourceDefinition{Key: "integration.permissions", Name: "Integration permissions", Scope: pbac.ResourceScopeTenant, Actions: []pbac.ActionDefinition{{Key: "read", Name: "Read"}}})
	resources, err := pbac.NewRegistryFromDefinitions(definitions)
	if err != nil {
		t.Fatal(err)
	}
	service := permission.New(permission.NewRepository(db), appdb.NewTransactor(db), discardOperationRecorder{}, discardSecurityRecorder{}, nil, resources, slog.Default())
	actorCtx := platformprincipal.SystemContext(ctx, "permission-integration")
	group, err := service.Create(actorCtx, permission.Input{Key: "integration.permissions", Name: "集成权限", NodeType: "group", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := service.Create(actorCtx, permission.Input{ParentID: &group.ID, Key: "integration.permissions.read", Name: "查询集成权限", NodeType: "permission", Resource: "integration.permissions", Action: "read", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := service.Tree(actorCtx, permission.TreeInput{Keyword: "查询集成权限", NodeTypes: []string{"permission"}, Statuses: []string{"active"}})
	if err != nil || len(tree) != 1 || len(tree[0].Children) != 1 {
		t.Fatalf("filtered permission tree=%+v err=%v", tree, err)
	}

	return leaf.ID
}

func testMenuLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB, permissionID string) {
	t.Helper()
	cfg := config.Config{Menu: config.Menu{CacheTTL: time.Minute, MaxNodes: 10000}, DistributedLock: config.DistributedLock{TTL: time.Second, RetryDelay: 10 * time.Millisecond}}
	service := menu.New(db, appdb.NewTransactor(db), nil, nil, discardOperationRecorder{}, discardSecurityRecorder{}, nil, nil, slog.Default(), cfg)
	actorCtx := platformprincipal.SystemContext(ctx, "menu-integration")
	root, err := service.Create(actorCtx, menu.Input{Key: "integration:menu", Name: "集成菜单", Type: "directory", Visible: true, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.Create(actorCtx, menu.Input{ParentID: &root.ID, Key: "integration:menu:page", Name: "集成页面", Type: "page", RoutePath: "/integration", Component: "integration/index", Visible: true, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	button, err := service.Create(actorCtx, menu.Input{ParentID: &page.ID, Key: "integration:menu:read", Name: "读取", Type: "button", PermissionID: &permissionID, Visible: true, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := service.Tree(actorCtx, menu.TreeInput{Keyword: "集成页面", Types: []string{"page"}})
	if err != nil || len(tree) != 1 || len(tree[0].Children) != 1 {
		t.Fatalf("filtered menu tree=%+v err=%v", tree, err)
	}
	updated, err := service.Update(actorCtx, menu.UpdateInput{ID: page.ID, ParentID: page.ParentID, Name: "集成页面更新", Type: page.Type, RoutePath: page.RoutePath, Component: page.Component, Visible: page.Visible, Status: page.Status, Version: page.Version})
	if err != nil || updated.Version != page.Version+1 {
		t.Fatalf("updated menu=%+v err=%v", updated, err)
	}
	if _, err := service.Update(actorCtx, menu.UpdateInput{ID: page.ID, ParentID: page.ParentID, Name: "旧版本", Type: page.Type, RoutePath: page.RoutePath, Component: page.Component, Visible: page.Visible, Status: page.Status, Version: page.Version}); !errors.Is(err, menu.ErrConflict) {
		t.Fatalf("stale menu update error=%v", err)
	}
	if err := service.Delete(actorCtx, page.ID, updated.Version); !errors.Is(err, menu.ErrConflict) {
		t.Fatalf("delete menu with child error=%v", err)
	}
	if err := service.Delete(actorCtx, button.ID, button.Version); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(actorCtx, page.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(actorCtx, root.ID, root.Version); err != nil {
		t.Fatal(err)
	}
	restored, err := service.Create(actorCtx, menu.Input{Key: root.Key, Name: "恢复菜单", Type: "directory", Visible: true, Status: "active"})
	if err != nil || restored.ID != root.ID || restored.Version <= root.Version {
		t.Fatalf("restored stable menu=%+v err=%v", restored, err)
	}
	if err := service.Delete(actorCtx, restored.ID, restored.Version); err != nil {
		t.Fatal(err)
	}
}

func testPlatformConfigLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	service := platformconfig.New(db, appdb.NewTransactor(db), nil, nil, discardOperationRecorder{}, discardSecurityRecorder{}, nil, slog.Default(), config.Config{PlatformConfig: config.PlatformConfig{CacheTTL: time.Minute}})
	actorCtx := platformprincipal.SystemContext(ctx, "config-integration")
	created, err := service.Create(actorCtx, platformconfig.Input{Key: "integration.feature", Name: "集成功能", Category: "feature", Value: []byte(`{"enabled":true}`), IsPublic: true, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.ValueType != "object" {
		t.Fatalf("created platform config=%+v", created)
	}
	public, err := service.GetPublic(ctx, "INTEGRATION.FEATURE")
	if err != nil || public.ValueType != "object" {
		t.Fatalf("public config=%+v err=%v", public, err)
	}
	page, err := service.Page(actorCtx, platformconfig.PageInput{Request: pagination.Request{Page: 1, PageSize: 20}, Categories: []string{"feature"}, ValueTypes: []string{"object"}, Statuses: []string{"active"}})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("platform config page=%+v err=%v", page, err)
	}
	updated, err := service.Update(actorCtx, platformconfig.UpdateInput{ID: created.ID, Name: created.Name, Category: created.Category, Value: []byte(`false`), IsPublic: false, Status: created.Status, Version: created.Version})
	if err != nil || updated.Version != 2 || updated.ValueType != "boolean" {
		t.Fatalf("updated platform config=%+v err=%v", updated, err)
	}
	if _, err := service.Update(actorCtx, platformconfig.UpdateInput{ID: created.ID, Name: created.Name, Category: created.Category, Value: []byte(`true`), Status: created.Status, Version: created.Version}); !errors.Is(err, platformconfig.ErrConflict) {
		t.Fatalf("stale platform config update error=%v", err)
	}
	if _, err := service.GetPublic(ctx, created.Key); !errors.Is(err, platformconfig.ErrNotFound) {
		t.Fatalf("private config public lookup error=%v", err)
	}
	if err := service.Delete(actorCtx, created.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	restored, err := service.Create(actorCtx, platformconfig.Input{Key: created.Key, Name: "恢复配置", Category: created.Category, Value: []byte(`true`), IsPublic: true, Status: "active"})
	if err != nil || restored.ID != created.ID || restored.Version <= updated.Version {
		t.Fatalf("restored stable platform config=%+v err=%v", restored, err)
	}
	if err := service.Delete(actorCtx, restored.ID, restored.Version); err != nil {
		t.Fatal(err)
	}
}

type integrationStorage struct{ deleted []string }

func (s *integrationStorage) Put(_ context.Context, input objectstorage.PutInput) (objectstorage.Info, error) {
	_, err := io.Copy(io.Discard, input.Body)
	return objectstorage.Info{Key: input.Key, Size: input.Size, ContentType: input.ContentType, ETag: "integration-etag"}, err
}
func (*integrationStorage) Get(context.Context, string) (*objectstorage.Object, error) {
	return nil, nil
}
func (*integrationStorage) Stat(context.Context, string) (objectstorage.Info, error) {
	return objectstorage.Info{}, nil
}
func (s *integrationStorage) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	return nil
}
func (*integrationStorage) Presign(_ context.Context, _ string, _ objectstorage.Operation, ttl time.Duration) (objectstorage.SignedURL, error) {
	return objectstorage.SignedURL{URL: "https://files.example/download", Method: "GET", ExpiresAt: time.Now().Add(ttl)}, nil
}

func testFileLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	storage := &integrationStorage{}
	cfg := config.Config{Files: config.Files{Enabled: true, MaxSizeBytes: 1024, DeletionInterval: time.Minute, DeletionRetryDelay: time.Minute, DeletionBatchSize: 10}, DistributedLock: config.DistributedLock{TTL: time.Second, RetryDelay: 10 * time.Millisecond}}
	service := files.New(db, appdb.NewTransactor(db), storage, nil, discardOperationRecorder{}, nil, slog.Default(), cfg)
	ownerCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "file-owner", Type: platformprincipal.TypeUser, TenantID: "file-tenant"})
	created, err := service.Upload(ownerCtx, files.UploadInput{Name: "../report.txt", Size: 5, Body: bytes.NewBufferString("hello")})
	if err != nil || created.OriginalName != "report.txt" || created.ContentType != "text/plain; charset=utf-8" || created.Version != 1 {
		t.Fatalf("uploaded file=%+v err=%v", created, err)
	}
	var uploadStatus string
	var uploadIntentDeletedAt *time.Time
	if err := db.QueryRowxContext(ctx, db.Rebind(`SELECT status,deleted_at FROM file_upload_intents WHERE id=?`), created.ID).Scan(&uploadStatus, &uploadIntentDeletedAt); err != nil || uploadStatus != "completed" || uploadIntentDeletedAt == nil {
		t.Fatalf("upload intent status=%q deleted_at=%v err=%v", uploadStatus, uploadIntentDeletedAt, err)
	}
	page, err := service.Page(ownerCtx, files.PageInput{Request: pagination.Request{Page: 1, PageSize: 20}, Keyword: "report", ContentTypes: []string{created.ContentType}})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("file page=%+v err=%v", page, err)
	}
	otherCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "other", Type: platformprincipal.TypeUser, TenantID: "other-tenant"})
	if _, err := service.Get(otherCtx, created.ID); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("cross-tenant file get error=%v", err)
	}
	if err := service.Delete(ownerCtx, created.ID, created.Version); err != nil {
		t.Fatal(err)
	}
	if len(storage.deleted) != 1 {
		t.Fatalf("deleted objects=%v", storage.deleted)
	}
	var deletedAt *time.Time
	if err := db.GetContext(ctx, &deletedAt, db.Rebind(`SELECT object_deleted_at FROM files WHERE id=?`), created.ID); err != nil || deletedAt == nil {
		t.Fatalf("object_deleted_at=%v err=%v", deletedAt, err)
	}
	orphanID := "orphan-" + created.ID
	orphanKey := "files/file-tenant/" + orphanID + "/orphan.txt"
	if err := appdb.NewTransactor(db).Within(ownerCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		_, err := tx.ExecContext(ownerCtx, tx.Rebind(`INSERT INTO file_upload_intents (id,tenant_id,object_key,status,attempts,last_error,next_attempt_at,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`), orphanID, "file-tenant", orphanKey, "pending", 0, "", now.Add(-time.Minute), now, "file-owner", now, "file-owner", 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessPendingUploads(ownerCtx); err != nil {
		t.Fatal(err)
	}
	if len(storage.deleted) != 2 || storage.deleted[1] != orphanKey {
		t.Fatalf("recovered orphan objects=%v", storage.deleted)
	}
	if err := db.QueryRowxContext(ctx, db.Rebind(`SELECT status,deleted_at FROM file_upload_intents WHERE id=?`), orphanID).Scan(&uploadStatus, &uploadIntentDeletedAt); err != nil || uploadStatus != "abandoned" || uploadIntentDeletedAt == nil {
		t.Fatalf("recovered upload intent status=%q deleted_at=%v err=%v", uploadStatus, uploadIntentDeletedAt, err)
	}
}

func testTenantLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	service := tenant.New(tenant.NewRepository(db), appdb.NewTransactor(db), nil, discardOperationRecorder{}, discardSecurityRecorder{}, staticUserResolver{id: "owner-1", username: "owner.one", name: "Owner One"}, slog.Default(), config.Config{Tenant: config.Tenant{CacheTTL: time.Minute}})
	createCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "owner-1", Type: platformprincipal.TypeUser})
	created, err := service.Create(createCtx, tenant.CreateInput{Code: "integration", Name: "Integration Tenant", OwnerUsername: "owner.one"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.Owner.ID != "owner-1" || created.Owner.Name != "Owner One" {
		t.Fatalf("created tenant = %+v", created)
	}
	adminView, err := service.AdminGet(createCtx, created.ID)
	if err != nil || adminView.ID != created.ID {
		t.Fatalf("admin tenant get=%+v err=%v", adminView, err)
	}
	adminPage, err := service.AdminPage(createCtx, tenant.PageInput{Request: pagination.Request{Page: 1, PageSize: 20}, IDs: []string{created.ID}, Statuses: []tenant.Status{tenant.StatusActive}})
	if err != nil || adminPage.Total != 1 || len(adminPage.Items) != 1 {
		t.Fatalf("admin tenant page=%+v err=%v", adminPage, err)
	}
	tenantCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "owner-1", Type: platformprincipal.TypeUser, TenantID: created.ID})
	adminUpdated, err := service.AdminUpdate(createCtx, tenant.UpdateInput{ID: created.ID, Name: "Platform Updated", Status: tenant.StatusActive, Version: created.Version})
	if err != nil || adminUpdated.Version != 2 {
		t.Fatalf("admin updated tenant=%+v err=%v", adminUpdated, err)
	}
	updated, err := service.Update(tenantCtx, tenant.UpdateInput{ID: created.ID, Name: "Integration Updated", Status: tenant.StatusActive, Version: adminUpdated.Version})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 3 {
		t.Fatalf("updated version = %d", updated.Version)
	}
	if _, err := service.Update(tenantCtx, tenant.UpdateInput{ID: created.ID, Name: "Stale", Status: tenant.StatusActive, Version: adminUpdated.Version}); !errors.Is(err, tenant.ErrConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	var membershipID string
	if err := db.GetContext(ctx, &membershipID, db.Rebind(`SELECT id FROM tenant_memberships WHERE tenant_id=? AND user_id=? AND deleted_at IS NULL`), created.ID, "owner-1"); err != nil {
		t.Fatal(err)
	}
	// This integration test exercises repository lifecycle behavior directly.
	// System callers are the only callers allowed to bypass endpoint data scopes.
	departmentCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "owner-1", Type: platformprincipal.TypeSystem, TenantID: created.ID, MembershipID: membershipID})
	members := tenant.NewMembershipService(tenant.NewRepository(db), appdb.NewTransactor(db), staticUserResolver{id: "member-2", username: "member.two", name: "Member Two"}, nil, config.Config{}, discardOperationRecorder{}, discardSecurityRecorder{}, nil)
	addedMember, err := members.Add(departmentCtx, "member.two")
	if err != nil || addedMember.UserID != "member-2" || addedMember.Version != 1 {
		t.Fatalf("added tenant member=%+v err=%v", addedMember, err)
	}
	memberRepository := tenant.NewRepository(db)
	visible, visibleTotal, err := memberRepository.PageMembers(ctx, created.ID, tenant.MemberPageInput{Request: pagination.Request{Page: 1, PageSize: 20}}, datapermission.SQLPredicate{Clause: "(tm.user_id = ?)", Args: []any{"member-2"}})
	if err != nil || visibleTotal != 1 || len(visible) != 1 || visible[0].ID != addedMember.ID {
		t.Fatalf("scoped tenant members=%+v total=%d err=%v", visible, visibleTotal, err)
	}
	hidden, hiddenTotal, err := memberRepository.PageMembers(ctx, created.ID, tenant.MemberPageInput{Request: pagination.Request{Page: 1, PageSize: 20}}, datapermission.SQLPredicate{Clause: "(1 = 0)"})
	if err != nil || hiddenTotal != 0 || len(hidden) != 0 {
		t.Fatalf("denied tenant members=%+v total=%d err=%v", hidden, hiddenTotal, err)
	}
	memberPage, err := members.Page(departmentCtx, tenant.MemberPageInput{Request: pagination.Request{Page: 1, PageSize: 20, Keyword: "member.two"}, UserIDs: []string{"member-2"}, Statuses: []tenant.Status{tenant.StatusActive}})
	if err != nil || memberPage.Total != 1 || len(memberPage.Items) != 1 {
		t.Fatalf("tenant member page=%+v err=%v", memberPage, err)
	}
	if err := members.Remove(departmentCtx, membershipID, 1); !errors.Is(err, tenant.ErrConflict) {
		t.Fatalf("remove final tenant administrator error=%v", err)
	}
	testMemberDataPermissionEnforcement(t, ctx, db, created.ID, membershipID, addedMember)
	departments := tenant.NewDepartmentService(db, appdb.NewTransactor(db), nil, discardOperationRecorder{}, staticUserResolver{id: "member-2", username: "member.two", name: "Member Two"}, config.Config{}, nil)
	root, err := departments.Create(departmentCtx, tenant.DepartmentInput{Code: "engineering", Name: "研发中心"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := departments.Create(departmentCtx, tenant.DepartmentInput{ParentID: &root.ID, Code: "backend", Name: "后端平台"})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := departments.Tree(departmentCtx, "backend")
	if err != nil || len(tree) != 1 || len(tree[0].Children) != 1 {
		t.Fatalf("department tree=%+v err=%v", tree, err)
	}
	if err := departments.SetMembers(departmentCtx, child.ID, []tenant.DepartmentMemberAssignment{{MembershipID: membershipID, IsPrimary: true}}); err != nil {
		t.Fatal(err)
	}
	if err := departments.Delete(departmentCtx, child.ID, child.Version); !errors.Is(err, tenant.ErrConflict) {
		t.Fatalf("delete assigned department error=%v", err)
	}
	if err := departments.SetMembers(departmentCtx, child.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := departments.Delete(departmentCtx, child.ID, child.Version); err != nil {
		t.Fatal(err)
	}
	if err := departments.Delete(departmentCtx, root.ID, root.Version); err != nil {
		t.Fatal(err)
	}
	if err := service.AdminDelete(createCtx, created.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(tenantCtx, created.ID); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("get deleted tenant error = %v", err)
	}
}

func testMemberDataPermissionEnforcement(t *testing.T, ctx context.Context, db *sqlx.DB, tenantID, ownerMembershipID string, member tenant.Member) {
	t.Helper()
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := datapermission.NewSchemaRegistry(tenant.NewMemberDataPermissionSchema())
	if err != nil {
		t.Fatal(err)
	}
	authenticated := true
	engine, err := datapermission.NewEngine(schemas, resources, []datapermission.Policy{{
		APIVersion: datapermission.PolicyAPIVersion,
		Kind:       datapermission.PolicyKind,
		Metadata:   datapermission.PolicyMetadata{Code: "integration-member-self", Name: "Integration member self scope"},
		Scope:      datapermission.PolicyBoundary{Type: datapermission.PolicyScopeTenant, TenantID: tenantID},
		Spec: datapermission.PolicySpec{
			Subject:   pbac.SubjectMatcher{Authenticated: &authenticated},
			Resource:  "tenant.member",
			Actions:   []string{"read", "list", "update", "remove"},
			Condition: "resource.owner_id == subject.id",
			Effect:    datapermission.EffectAllow,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	scopes := datapermission.NewService(db, engine)
	service := tenant.NewMembershipService(tenant.NewRepository(db), appdb.NewTransactor(db), staticUserResolver{id: member.UserID, username: member.Username, name: member.DisplayName}, nil, config.Config{}, discardOperationRecorder{}, discardSecurityRecorder{}, scopes)
	principal := platformprincipal.Principal{ID: member.UserID, Type: platformprincipal.TypeUser, TenantID: tenantID, MembershipID: member.ID}
	endpointContext := func(action string, mode accesscontrol.DataPermissionMode) context.Context {
		requestCtx := platformprincipal.WithContext(ctx, principal)
		return accesscontrol.WithEndpoint(requestCtx, accesscontrol.Endpoint{Resource: "tenant.member", Action: action, DataPermission: mode})
	}

	visible, err := service.Get(endpointContext("read", accesscontrol.DataPermissionRequired), member.ID)
	if err != nil || visible.ID != member.ID {
		t.Fatalf("data-scoped member get=%+v err=%v", visible, err)
	}
	if _, err := service.Get(endpointContext("read", accesscontrol.DataPermissionRequired), ownerMembershipID); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("hidden member get error=%v", err)
	}
	page, err := service.Page(endpointContext("list", accesscontrol.DataPermissionRequired), tenant.MemberPageInput{Request: pagination.Request{Page: 1, PageSize: 20}})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != member.ID {
		t.Fatalf("data-scoped member page=%+v err=%v", page, err)
	}
	if _, err := service.UpdateStatus(endpointContext("update", accesscontrol.DataPermissionRequired), ownerMembershipID, tenant.StatusDisabled, 1); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("hidden member update error=%v", err)
	}
	updated, err := service.UpdateStatus(endpointContext("update", accesscontrol.DataPermissionRequired), member.ID, tenant.StatusActive, member.Version)
	if err != nil || updated.Version != member.Version+1 {
		t.Fatalf("data-scoped member update=%+v err=%v", updated, err)
	}
	if err := service.Remove(endpointContext("remove", accesscontrol.DataPermissionRequired), ownerMembershipID, 1); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("hidden member remove error=%v", err)
	}
	if err := service.Remove(endpointContext("remove", accesscontrol.DataPermissionRequired), member.ID, updated.Version); err != nil {
		t.Fatalf("data-scoped member remove: %v", err)
	}
}

type staticUserResolver struct{ id, username, name string }

type staticActorResolver map[string]string

func (r staticActorResolver) ResolveUserIDs(_ context.Context, ids []string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	for _, id := range ids {
		if name := r[id]; name != "" {
			names[id] = name
		}
	}
	return names, nil
}

func (r staticUserResolver) ResolveUsername(context.Context, string) (tenant.UserSnapshot, error) {
	return tenant.UserSnapshot{ID: r.id, Username: r.username, DisplayName: r.name}, nil
}

func testPostgresAuditInfrastructure(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	_, err := db.ExecContext(ctx, `CREATE TABLE audit_contract_records (
		id text PRIMARY KEY,
		name text NOT NULL,
		created_at timestamptz NOT NULL,
		created_by text NOT NULL,
		updated_at timestamptz NOT NULL,
		updated_by text NOT NULL,
		version bigint NOT NULL,
		deleted_at timestamptz,
		deleted_by text
	)`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS audit_contract_records") })
	if _, err := db.ExecContext(ctx, `SELECT app_enable_audit('audit_contract_records')`); err != nil {
		t.Fatal(err)
	}

	transactor := appdb.NewTransactor(db)
	actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "user-42", Type: platformprincipal.TypeUser})
	if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		_, execErr := tx.ExecContext(actorCtx, `INSERT INTO audit_contract_records (id, name, created_at, created_by, updated_at, updated_by, version) VALUES ($1, $2, now(), 'ignored', now(), 'ignored', 99)`, "record-1", "first")
		return execErr
	}); err != nil {
		t.Fatal(err)
	}
	var created struct {
		CreatedBy string `db:"created_by"`
		UpdatedBy string `db:"updated_by"`
		Version   int64  `db:"version"`
	}
	if err := db.GetContext(ctx, &created, `SELECT created_by, updated_by, version FROM audit_contract_records WHERE id = $1`, "record-1"); err != nil {
		t.Fatal(err)
	}
	if created.CreatedBy != "user-42" || created.UpdatedBy != "user-42" || created.Version != 1 {
		t.Fatalf("created audit = %+v", created)
	}

	if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		result, execErr := tx.ExecContext(actorCtx, `UPDATE audit_contract_records SET name = $1 WHERE id = $2 AND version = $3 AND deleted_at IS NULL`, "second", "record-1", 1)
		if execErr != nil {
			return execErr
		}
		rows, execErr := result.RowsAffected()
		if execErr != nil {
			return execErr
		}
		if rows != 1 {
			return fmt.Errorf("optimistic update affected %d rows", rows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		_, execErr := tx.ExecContext(actorCtx, `UPDATE audit_contract_records SET deleted_at = now() WHERE id = $1 AND version = $2`, "record-1", 2)
		return execErr
	}); err != nil {
		t.Fatal(err)
	}
	var deleted struct {
		UpdatedBy string     `db:"updated_by"`
		DeletedBy string     `db:"deleted_by"`
		Version   int64      `db:"version"`
		DeletedAt *time.Time `db:"deleted_at"`
	}
	if err := db.GetContext(ctx, &deleted, `SELECT updated_by, deleted_by, version, deleted_at FROM audit_contract_records WHERE id = $1`, "record-1"); err != nil {
		t.Fatal(err)
	}
	if deleted.DeletedAt == nil || deleted.DeletedBy != "user-42" || deleted.UpdatedBy != "user-42" || deleted.Version != 3 {
		t.Fatalf("deleted audit = %+v", deleted)
	}
	if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		_, execErr := tx.ExecContext(actorCtx, `DELETE FROM audit_contract_records WHERE id = $1`, "record-1")
		return execErr
	}); err == nil {
		t.Fatal("physical DELETE unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE audit_contract_records"); err != nil {
		t.Fatal(err)
	}
}

func testMySQLAuditInfrastructure(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "mysql-auditor", Type: platformprincipal.TypeSystem})
	transactor := appdb.NewTransactor(db)
	if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(actorCtx, `INSERT INTO permissions (id,parent_id,permission_key,name,node_type,resource,action,description,sort_order,status,is_system,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP(6),'ignored',CURRENT_TIMESTAMP(6),'ignored',99)`, "mysql-audit-record", nil, "test.mysql-audit", "audit", "leaf", "test", "audit", "", 0, "active", false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var created struct {
		CreatedBy string `db:"created_by"`
		UpdatedBy string `db:"updated_by"`
		Version   int64  `db:"version"`
	}
	if err := db.GetContext(ctx, &created, `SELECT created_by,updated_by,version FROM permissions WHERE id=?`, "mysql-audit-record"); err != nil {
		t.Fatal(err)
	}
	if created.CreatedBy != "mysql-auditor" || created.UpdatedBy != "mysql-auditor" || created.Version != 1 {
		t.Fatalf("created audit = %+v", created)
	}
	if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(actorCtx, `UPDATE permissions SET deleted_at=CURRENT_TIMESTAMP(6),deleted_by='ignored',version=99 WHERE id=? AND version=1`, "mysql-audit-record")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var deleted struct {
		UpdatedBy string     `db:"updated_by"`
		DeletedBy string     `db:"deleted_by"`
		Version   int64      `db:"version"`
		DeletedAt *time.Time `db:"deleted_at"`
	}
	if err := db.GetContext(ctx, &deleted, `SELECT updated_by,deleted_by,version,deleted_at FROM permissions WHERE id=?`, "mysql-audit-record"); err != nil {
		t.Fatal(err)
	}
	if deleted.DeletedAt == nil || deleted.DeletedBy != "mysql-auditor" || deleted.UpdatedBy != "mysql-auditor" || deleted.Version != 2 {
		t.Fatalf("deleted audit = %+v", deleted)
	}
	if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(actorCtx, `DELETE FROM permissions WHERE id=?`, "mysql-audit-record")
		return err
	}); err == nil {
		t.Fatal("physical DELETE unexpectedly succeeded")
	}
}

func startDatabase(t *testing.T, ctx context.Context, databaseType string) (string, string) {
	t.Helper()
	switch databaseType {
	case "postgres":
		container, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("app"), postgres.WithUsername("app"), postgres.WithPassword("app"), postgres.BasicWaitStrategies(), postgres.WithSQLDriver("pgx"))
		if err != nil {
			t.Fatal(err)
		}
		testcontainers.CleanupContainer(t, container)
		dsn, err := container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			t.Fatal(err)
		}
		return dsn, dsn
	case "mysql":
		configPath, err := filepath.Abs(filepath.Join("testdata", "mysql.cnf"))
		if err != nil {
			t.Fatal(err)
		}
		container, err := mysql.Run(ctx, "mysql:8.4", mysql.WithDatabase("app"), mysql.WithUsername("app"), mysql.WithPassword("app"), mysql.WithConfigFile(configPath))
		if err != nil {
			t.Fatal(err)
		}
		testcontainers.CleanupContainer(t, container)
		dsn, err := container.ConnectionString(ctx, "parseTime=true")
		if err != nil {
			t.Fatal(err)
		}
		migrationDSN, err := container.ConnectionString(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return dsn, "mysql://" + migrationDSN
	default:
		t.Fatal(fmt.Errorf("unsupported database %q", databaseType))
		return "", ""
	}
}
