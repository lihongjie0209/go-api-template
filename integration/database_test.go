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

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/application"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	userauthentication "github.com/lihongjie0209/go-api-template/internal/authentication"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	appdb "github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/datalifecycle"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/dictionary"
	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	"github.com/lihongjie0209/go-api-template/internal/files"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/migration"
	"github.com/lihongjie0209/go-api-template/internal/navigation"
	"github.com/lihongjie0209/go-api-template/internal/objectstorage"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/permission"
	"github.com/lihongjie0209/go-api-template/internal/platformconfig"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/lihongjie0209/go-api-template/internal/resourcecatalog"
	"github.com/lihongjie0209/go-api-template/internal/scheduler"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
	"github.com/lihongjie0209/go-api-template/internal/tenant"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"go.uber.org/fx"
	"google.golang.org/protobuf/proto"
)

type integrationLifecycleLocker struct{}

type integrationLifecycle struct{}

func (integrationLifecycle) Append(fx.Hook) {}

type allowCapabilityAuthorizer struct{}

func (allowCapabilityAuthorizer) Authorize(context.Context, platformprincipal.Principal, platformauthz.Requirement) error {
	return nil
}

func (allowCapabilityAuthorizer) Snapshot(ctx context.Context, _ platformprincipal.Principal) (context.Context, string, error) {
	return ctx, "integration-allow", nil
}

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
			testResourceCatalogLifecycle(t, ctx, db)
			permissionID := testPermissionLifecycle(t, ctx, db)
			testManagedAuthorizationLifecycle(t, ctx, db, permissionID)
			testTenantAuthorizationLifecycle(t, ctx, db, permissionID)
			testApplicationNavigationLifecycle(t, ctx, db)
			testPlatformConfigLifecycle(t, ctx, db)
			testScheduledJobLifecycle(t, ctx, db)
			testDictionaryLifecycle(t, ctx, db)
			testDataPermissionLifecycle(t, ctx, db)
			testDeletedDepartmentExcludedFromDataPermission(t, ctx, db)
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

func testResourceCatalogLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	actorCtx := platformprincipal.SystemContext(ctx, "integration-catalog-test")
	transactor := appdb.NewTransactor(db)
	err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		_, insertErr := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO authorization_resources (id,resource_key,name,description,scope,source,status,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,'manual','active',?,?,?,?,1)`), "manual-resource", "manual.feature", "Manual feature", "", "tenant", now, "integration-catalog-test", now, "integration-catalog-test")
		if insertErr != nil {
			return insertErr
		}
		_, insertErr = tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO authorization_resource_actions (id,resource_id,action_key,name,description,source,status,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,'manual','active',?,?,?,?,1)`), "manual-action", "manual-resource", "view", "View", "", now, "integration-catalog-test", now, "integration-catalog-test")
		return insertErr
	})
	if err != nil {
		t.Fatalf("insert manual catalog records: %v", err)
	}
	first, err := resourceaction.NewRegistry([]resourceaction.ResourceDefinition{{Key: "integration.catalog", Name: "Catalog", Scope: resourceaction.ResourceScopeTenant, Actions: []resourceaction.ActionDefinition{{Key: "read", Name: "Read"}}}})
	if err != nil {
		t.Fatal(err)
	}
	const firstBuild = "2026-10-13T08:00:00Z"
	if err := resourcecatalog.New(db, transactor, first).Sync(ctx, firstBuild, "catalog-1"); err != nil {
		t.Fatalf("sync first code registry: %v", err)
	}
	var version int64
	if err := db.GetContext(ctx, &version, db.Rebind(`SELECT version FROM authorization_resources WHERE resource_key=?`), "integration.catalog"); err != nil || version != 1 {
		t.Fatalf("first resource version=%d err=%v", version, err)
	}
	if err := resourcecatalog.New(db, transactor, first).Sync(ctx, firstBuild, "catalog-1"); err != nil {
		t.Fatalf("repeat code registry sync: %v", err)
	}
	if err := db.GetContext(ctx, &version, db.Rebind(`SELECT version FROM authorization_resources WHERE resource_key=?`), "integration.catalog"); err != nil || version != 1 {
		t.Fatalf("repeat sync must preserve audit version: version=%d err=%v", version, err)
	}
	second, err := resourceaction.NewRegistry([]resourceaction.ResourceDefinition{{Key: "integration.catalog", Name: "Updated catalog", Scope: resourceaction.ResourceScopeTenant, Actions: []resourceaction.ActionDefinition{{Key: "update", Name: "Update"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := resourcecatalog.New(db, transactor, second).Sync(ctx, "2026-10-13T08:01:00Z", "catalog-2"); err != nil {
		t.Fatalf("sync changed code registry: %v", err)
	}
	var codeResource struct {
		Name    string `db:"name"`
		Status  string `db:"status"`
		Version int64  `db:"version"`
	}
	if err := db.GetContext(ctx, &codeResource, db.Rebind(`SELECT name,status,version FROM authorization_resources WHERE resource_key=?`), "integration.catalog"); err != nil || codeResource.Name != "Updated catalog" || codeResource.Status != "active" || codeResource.Version != 2 {
		t.Fatalf("code resource was not replaced: %#v err=%v", codeResource, err)
	}
	var actions []struct {
		Key    string `db:"action_key"`
		Status string `db:"status"`
	}
	if err := db.SelectContext(ctx, &actions, db.Rebind(`SELECT a.action_key,a.status FROM authorization_resource_actions a JOIN authorization_resources r ON r.id=a.resource_id WHERE r.resource_key=? ORDER BY a.action_key`), "integration.catalog"); err != nil || len(actions) != 2 || actions[0].Key != "read" || actions[0].Status != "disabled" || actions[1].Key != "update" || actions[1].Status != "active" {
		t.Fatalf("code actions were not reconciled: %#v err=%v", actions, err)
	}
	var manualStatus string
	if err := db.GetContext(ctx, &manualStatus, `SELECT status FROM authorization_resources WHERE id='manual-resource'`); err != nil || manualStatus != "active" {
		t.Fatalf("manual resource changed: status=%q err=%v", manualStatus, err)
	}
	if err := db.GetContext(ctx, &manualStatus, `SELECT status FROM authorization_resource_actions WHERE id='manual-action'`); err != nil || manualStatus != "active" {
		t.Fatalf("manual action changed: status=%q err=%v", manualStatus, err)
	}
	if err := resourcecatalog.New(db, transactor, first).Sync(ctx, firstBuild, "catalog-1"); !errors.Is(err, resourcecatalog.ErrStaleBuild) {
		t.Fatalf("old build unexpectedly replaced the newer catalog: %v", err)
	}
	empty, err := resourceaction.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := resourcecatalog.New(db, transactor, empty).Sync(ctx, "2026-10-13T08:02:00Z", "catalog-3"); err != nil {
		t.Fatalf("retire removed code resources: %v", err)
	}
	if err := db.GetContext(ctx, &codeResource, db.Rebind(`SELECT name,status,version FROM authorization_resources WHERE resource_key=?`), "integration.catalog"); err != nil || codeResource.Status != "disabled" {
		t.Fatalf("removed code resource was not disabled: %#v err=%v", codeResource, err)
	}
	if err := db.GetContext(ctx, &manualStatus, `SELECT status FROM authorization_resources WHERE id='manual-resource'`); err != nil || manualStatus != "active" {
		t.Fatalf("manual resource changed during code retirement: status=%q err=%v", manualStatus, err)
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
	updateTarget, err := service.UpdateTarget(actorCtx, definition.ID)
	if err != nil || updateTarget.ID != definition.ID || updateTarget.Code != definition.Code || updateTarget.Version != definition.Version {
		t.Fatalf("dictionary update target=%+v err=%v", updateTarget, err)
	}
	deleteTarget, err := service.DeleteTarget(actorCtx, definition.ID)
	if err != nil || deleteTarget.ID != definition.ID || deleteTarget.Code != definition.Code || deleteTarget.Version != definition.Version {
		t.Fatalf("dictionary delete target=%+v err=%v", deleteTarget, err)
	}
	root, err := service.CreateItem(actorCtx, dictionary.ItemInput{DictionaryID: definition.ID, Code: "cn", Name: "China", SortOrder: 1})
	if err != nil {
		t.Fatalf("create root dictionary item: %v", err)
	}
	child, err := service.CreateItem(actorCtx, dictionary.ItemInput{DictionaryID: definition.ID, ParentID: &root.ID, Code: "gd", Name: "Guangdong", Value: "guangdong-value", SortOrder: 1})
	if err != nil {
		t.Fatalf("create child dictionary item: %v", err)
	}
	if child.ParentName != root.Name {
		t.Fatalf("created child missing parent display name: %#v", child)
	}
	itemUpdateTarget, err := service.ItemUpdateTarget(actorCtx, child.ID)
	if err != nil || itemUpdateTarget.ID != child.ID || itemUpdateTarget.DictionaryID != definition.ID || itemUpdateTarget.ParentName != root.Name || itemUpdateTarget.Version != child.Version {
		t.Fatalf("dictionary item update target=%+v err=%v", itemUpdateTarget, err)
	}
	itemDeleteTarget, err := service.ItemDeleteTarget(actorCtx, child.ID)
	if err != nil || itemDeleteTarget.ID != child.ID || itemDeleteTarget.DictionaryID != definition.ID || itemDeleteTarget.Version != child.Version {
		t.Fatalf("dictionary item delete target=%+v err=%v", itemDeleteTarget, err)
	}
	childPage, err := service.PageItems(actorCtx, dictionary.ItemPageInput{Request: pagination.Request{Page: 1, PageSize: 20}, DictionaryID: definition.ID, ParentID: &root.ID})
	if err != nil || childPage.Total != 1 || len(childPage.Items) != 1 || childPage.Items[0].ParentName != root.Name {
		t.Fatalf("dictionary page missing parent display name: %#v err=%v", childPage, err)
	}
	storedChild, err := service.GetItem(actorCtx, child.ID)
	if err != nil || storedChild.ParentName != root.Name {
		t.Fatalf("dictionary detail missing parent display name: %#v err=%v", storedChild, err)
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
	valueFiltered, err := service.Query(ctx, dictionary.Query{Code: "test.region", Values: []string{"guangdong-value"}})
	if err != nil || len(valueFiltered.Items) != 1 || len(valueFiltered.Items[0].Children) != 1 || valueFiltered.Items[0].Children[0].ID != child.ID {
		t.Fatalf("tree value lookup must retain ancestors: %#v err=%v", valueFiltered.Items, err)
	}
	rootBySelectableValue, err := service.Query(ctx, dictionary.Query{Code: "test.region", Values: []string{root.ID}})
	if err != nil || len(rootBySelectableValue.Items) != 1 || rootBySelectableValue.Items[0].ID != root.ID || len(rootBySelectableValue.Items[0].Children) != 0 {
		t.Fatalf("empty value must be selectable by item ID: %#v err=%v", rootBySelectableValue.Items, err)
	}
	if _, err := service.UpdateItem(actorCtx, dictionary.ItemUpdate{ID: child.ID, ParentID: &root.ID, Name: "Guangdong", Value: "", SortOrder: 1, Version: child.Version + 1}); !errors.Is(err, dictionary.ErrStaleVersion) {
		t.Fatalf("stale dictionary item update error=%v", err)
	}
	stored, err := service.GetItem(actorCtx, root.ID)
	if err != nil || stored.CreatedBy != "dictionary-test" || stored.Version != 1 {
		t.Fatalf("dictionary audit fields not maintained: %#v err=%v", stored, err)
	}
	otherRoot, err := service.CreateItem(actorCtx, dictionary.ItemInput{DictionaryID: definition.ID, Code: "us", Name: "United States", SortOrder: 2})
	if err != nil {
		t.Fatalf("create sibling dictionary root: %v", err)
	}
	parentCandidates, err := service.PageItems(actorCtx, dictionary.ItemPageInput{Request: pagination.Request{Page: 1, PageSize: 20}, DictionaryID: definition.ID, ExcludeSubtreeID: root.ID})
	if err != nil || parentCandidates.Total != 1 || len(parentCandidates.Items) != 1 || parentCandidates.Items[0].ID != otherRoot.ID {
		t.Fatalf("root and descendants must not be parent candidates: %#v err=%v", parentCandidates, err)
	}
	childParentCandidates, err := service.PageItems(actorCtx, dictionary.ItemPageInput{Request: pagination.Request{Page: 1, PageSize: 20}, DictionaryID: definition.ID, ExcludeSubtreeID: child.ID})
	if err != nil || childParentCandidates.Total != 2 || len(childParentCandidates.Items) != 2 {
		t.Fatalf("child exclusion must retain other parents: %#v err=%v", childParentCandidates, err)
	}
	enumDefinition, err := service.CreateDefinition(actorCtx, dictionary.DefinitionInput{Code: "test.gender", Name: "Gender", Kind: dictionary.KindEnum, Source: dictionary.SourceStatic, Status: "active"})
	if err != nil {
		t.Fatalf("create enum dictionary: %v", err)
	}
	for index, code := range []string{"m", "f", "u"} {
		if _, err := service.CreateItem(actorCtx, dictionary.ItemInput{DictionaryID: enumDefinition.ID, Code: code, Name: strings.ToUpper(code), Value: "label-" + code, SortOrder: int64(index)}); err != nil {
			t.Fatalf("create enum item: %v", err)
		}
	}
	enumPage, err := service.Query(ctx, dictionary.Query{Code: "test.gender", Page: 2, PageSize: 2})
	if err != nil || enumPage.Total != 3 || len(enumPage.Items) != 1 || enumPage.Page != 2 {
		t.Fatalf("unexpected enum page: %#v err=%v", enumPage, err)
	}
	selectedEnum, err := service.Query(ctx, dictionary.Query{Code: "test.gender", Values: []string{"label-f"}})
	if err != nil || selectedEnum.Total != 1 || len(selectedEnum.Items) != 1 || selectedEnum.Items[0].Code != "f" {
		t.Fatalf("enum value lookup must be exact and independent of code: %#v err=%v", selectedEnum, err)
	}
	if _, err := service.CreateItem(actorCtx, dictionary.ItemInput{DictionaryID: enumDefinition.ID, Code: "literal", Name: "literal %_! marker"}); err != nil {
		t.Fatalf("create literal wildcard item: %v", err)
	}
	for _, keyword := range []string{"%", "_", "!", "%_!"} {
		literal, queryErr := service.Query(ctx, dictionary.Query{Code: "test.gender", Keyword: keyword})
		if queryErr != nil || literal.Total != 1 || len(literal.Items) != 1 || literal.Items[0].Code != "literal" {
			t.Fatalf("literal wildcard keyword %q matched unexpected items: %#v err=%v", keyword, literal, queryErr)
		}
	}
	if _, err := service.CreateItem(actorCtx, dictionary.ItemInput{DictionaryID: definition.ID, Code: "duplicate-value", Name: "Duplicate", Value: "guangdong-value"}); !errors.Is(err, dictionary.ErrConflict) {
		t.Fatalf("duplicate selectable value create error=%v", err)
	}
	if _, err := service.UpdateItem(actorCtx, dictionary.ItemUpdate{ID: otherRoot.ID, Name: otherRoot.Name, Value: root.ID, Version: otherRoot.Version}); !errors.Is(err, dictionary.ErrConflict) {
		t.Fatalf("item ID fallback collision update error=%v", err)
	}
	if err := service.DeleteItem(actorCtx, child.ID, child.Version+1); !errors.Is(err, dictionary.ErrStaleVersion) {
		t.Fatalf("stale dictionary item delete error=%v", err)
	}
	if err := service.DeleteItem(actorCtx, child.ID, child.Version); err != nil {
		t.Fatalf("delete dictionary leaf: %v", err)
	}
	if _, err := service.ItemDeleteTarget(actorCtx, child.ID); !errors.Is(err, dictionary.ErrNotFound) {
		t.Fatalf("deleted dictionary item target error=%v", err)
	}
	if _, err := service.CreateItem(actorCtx, dictionary.ItemInput{DictionaryID: definition.ID, Code: "reused-value", Name: "Reused", Value: "guangdong-value"}); err != nil {
		t.Fatalf("soft-deleted selectable value should be reusable: %v", err)
	}
	results := make(chan error, 2)
	for _, code := range []string{"concurrent-a", "concurrent-b"} {
		go func(code string) {
			_, createErr := service.CreateItem(actorCtx, dictionary.ItemInput{DictionaryID: enumDefinition.ID, Code: code, Name: code, Value: "concurrent-value"})
			results <- createErr
		}(code)
	}
	successes, conflicts := 0, 0
	for range 2 {
		switch createErr := <-results; {
		case createErr == nil:
			successes++
		case errors.Is(createErr, dictionary.ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent dictionary create error=%v", createErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent duplicate values successes=%d conflicts=%d", successes, conflicts)
	}
}

func testDataPermissionLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	tenantID := uuid.NewString()
	fixtureCtx := platformprincipal.SystemContext(ctx, "data-permission-integration")
	if err := appdb.NewTransactor(db).Within(fixtureCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		_, err := tx.ExecContext(fixtureCtx, tx.Rebind(`INSERT INTO tenants(id,code,name,description,status,owner_user_id,owner_name,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`), tenantID, "data-permission-integration", "Data Permission Integration", "", "active", "system", "System", now, "data-permission-integration", now, "data-permission-integration")
		return err
	}); err != nil {
		t.Fatalf("create data permission tenant fixture: %v", err)
	}
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
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	if err != nil {
		t.Fatalf("create data permission resource registry: %v", err)
	}
	engine, err := datapermission.NewRuntimeEngine(schemas, resources)
	if err != nil {
		t.Fatalf("create data permission engine: %v", err)
	}
	repository := datapermission.NewRepository(db)
	loader := datapermission.NewRuntimeLoader(repository, engine)
	service := datapermission.NewLifecycleService(repository, appdb.NewTransactor(db), schemas, resources, loader, nil, nil, nil)
	_, initialSnapshotRevision, err := engine.Pin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	authenticated := true
	document := datapermission.Policy{
		APIVersion: datapermission.PolicyAPIVersion,
		Kind:       datapermission.PolicyKind,
		Metadata:   datapermission.PolicyMetadata{Code: "integration-member-owner", Name: "Integration member owner"},
		Scope:      datapermission.PolicyBoundary{Type: datapermission.PolicyScopeTenant, TenantID: tenantID},
		Spec: datapermission.PolicySpec{
			Subject:   resourceaction.SubjectMatcher{Authenticated: &authenticated},
			Resource:  "tenant.member",
			Actions:   []string{"read", "list"},
			Condition: "resource.owner_id == subject.id",
			Effect:    datapermission.EffectAllow,
		},
	}
	missingTenantDocument := document
	missingTenantDocument.Scope.TenantID = uuid.NewString()
	missingTenantCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "data-admin", Type: platformprincipal.TypeUser, TenantID: missingTenantDocument.Scope.TenantID})
	if _, _, err := service.Create(missingTenantCtx, missingTenantDocument); !errors.Is(err, datapermission.ErrInvalidPolicy) {
		t.Fatalf("create policy for missing tenant error=%v", err)
	}
	actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "data-admin", Type: platformprincipal.TypeUser, TenantID: tenantID})
	created, version, err := service.Create(actorCtx, document)
	if err != nil || created.Version != 1 || version.Status != datapermission.VersionDraft {
		t.Fatalf("create data permission policy=%+v version=%+v err=%v", created, version, err)
	}
	storedDraft, err := service.GetVersion(actorCtx, created.ID, version.VersionNumber)
	if err != nil {
		t.Fatalf("load saved data permission draft: %v", err)
	}
	draftSimulation, err := datapermission.NewSimulator(schemas, resources, engine).SimulateVersion(ctx, storedDraft, datapermission.SavedSimulationInput{
		ExpectedScope: datapermission.PolicyScopeTenant, ActorTenantID: tenantID, PolicyCode: created.Code,
		Action: "read", Subject: resourceaction.Subject{ID: "member-1", Type: "user", Authenticated: true, TenantID: tenantID},
		Resource:          resourceaction.Resource{Type: "tenant.member", TenantID: tenantID},
		SubjectAttributes: datapermission.SubjectAttributes{"id": "member-1"}, ResourceAttributes: datapermission.ResourceAttributes{"owner_id": "member-1"},
	})
	if err != nil || draftSimulation.Baseline.CurrentAllowed || !draftSimulation.Candidate.CurrentAllowed || !draftSimulation.Changed {
		t.Fatalf("saved draft simulation=%+v err=%v", draftSimulation, err)
	}
	published, err := service.Publish(actorCtx, created.ID, 1, created.Version)
	if err != nil || !published.PublishedVersionNumber.Valid || published.PublishedVersionNumber.Int64 != 1 {
		t.Fatalf("publish data permission policy=%+v err=%v", published, err)
	}
	_, publishedSnapshotRevision, err := engine.Pin(ctx)
	if err != nil || publishedSnapshotRevision == initialSnapshotRevision {
		t.Fatalf("published policy must advance snapshot revision: before=%q after=%q err=%v", initialSnapshotRevision, publishedSnapshotRevision, err)
	}
	predicate, err := engine.CompileSQL(ctx, "tenant.member", "read", resourceaction.Subject{ID: "member-1", Type: "user", Authenticated: true, TenantID: tenantID}, datapermission.SubjectAttributes{"id": "member-1"})
	if err != nil || predicate.Clause != "(tm.user_id = ?)" || len(predicate.Args) != 1 || predicate.Args[0] != "member-1" {
		t.Fatalf("compiled data permission predicate=%+v err=%v", predicate, err)
	}
	beforeRevision, err := repository.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	setTenantStatus := func(status tenant.Status) {
		t.Helper()
		if err := appdb.NewTransactor(db).Within(fixtureCtx, nil, func(tx *sqlx.Tx) error {
			result, err := tx.ExecContext(fixtureCtx, tx.Rebind(`UPDATE tenants SET status=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND deleted_at IS NULL`), status, time.Now(), "data-permission-integration", tenantID)
			if err != nil {
				return err
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if rows != 1 {
				return errors.New("tenant fixture status update affected no rows")
			}
			return nil
		}); err != nil {
			t.Fatalf("change data permission tenant status: %v", err)
		}
	}
	setTenantStatus(tenant.StatusDisabled)
	afterRevision, err := repository.Revision(ctx)
	if err != nil || afterRevision == beforeRevision {
		t.Fatalf("tenant status must advance data policy revision: before=%q after=%q err=%v", beforeRevision, afterRevision, err)
	}
	if err := loader.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	inactivePredicate, err := engine.CompileSQL(ctx, "tenant.member", "read", resourceaction.Subject{ID: "member-1", Type: "user", Authenticated: true, TenantID: tenantID}, datapermission.SubjectAttributes{"id": "member-1"})
	if err != nil || inactivePredicate.Clause != "(1 = 0)" {
		t.Fatalf("disabled tenant data permission predicate=%+v err=%v", inactivePredicate, err)
	}
	setTenantStatus(tenant.StatusActive)
	if err := loader.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	reactivatedPredicate, err := engine.CompileSQL(ctx, "tenant.member", "read", resourceaction.Subject{ID: "member-1", Type: "user", Authenticated: true, TenantID: tenantID}, datapermission.SubjectAttributes{"id": "member-1"})
	if err != nil || reactivatedPredicate.Clause != "(tm.user_id = ?)" {
		t.Fatalf("reactivated tenant data permission predicate=%+v err=%v", reactivatedPredicate, err)
	}
	disabled, err := service.SetStatus(actorCtx, datapermission.SetPolicyStatusInput{PolicyID: created.ID, Status: datapermission.StatusDisabled, ExpectedPolicyVersion: published.Version})
	if err != nil || disabled.Status != datapermission.StatusDisabled {
		t.Fatalf("disable data permission policy=%+v err=%v", disabled, err)
	}
	_, disabledSnapshotRevision, err := engine.Pin(ctx)
	if err != nil || disabledSnapshotRevision == publishedSnapshotRevision {
		t.Fatalf("disabled policy must advance snapshot revision: published=%q disabled=%q err=%v", publishedSnapshotRevision, disabledSnapshotRevision, err)
	}
	denied, err := engine.CompileSQL(ctx, "tenant.member", "read", resourceaction.Subject{ID: "member-1", Type: "user", Authenticated: true, TenantID: tenantID}, datapermission.SubjectAttributes{"id": "member-1"})
	if err != nil || denied.Clause != "(1 = 0)" {
		t.Fatalf("disabled data permission predicate=%+v err=%v", denied, err)
	}
}

func testDeletedDepartmentExcludedFromDataPermission(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	tenantID, departmentID, membershipID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	actorCtx := platformprincipal.SystemContext(ctx, "data-permission-department-fixture")
	if err := appdb.NewTransactor(db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		if _, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO tenants(id,code,name,description,status,owner_user_id,owner_name,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`), tenantID, tenantID, "Department Scope Fixture", "", "active", "user-1", "User One", now, "system", now, "system"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO tenant_departments(id,tenant_id,code,name,sort_order,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,1)`), departmentID, tenantID, "scope", "Scope", 0, now, "system", now, "system"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO tenant_memberships(id,tenant_id,user_id,username,display_name,status,joined_at,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`), membershipID, tenantID, "user-1", "scope-user", "Scope User", "active", now, now, "system", now, "system"); err != nil {
			return err
		}
		_, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO tenant_department_members(id,tenant_id,department_id,membership_id,is_primary,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,1)`), uuid.NewString(), tenantID, departmentID, membershipID, false, now, "system", now, "system")
		return err
	}); err != nil {
		t.Fatalf("seed department projection fixture: %v", err)
	}
	schema, err := datapermission.NewSchema("tenant.member", map[string]datapermission.Field{"department_id": {Column: "tm.department_id", Type: datapermission.ValueTypeText}})
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := datapermission.NewSchemaRegistry(schema)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := datapermission.NewEngine(schemas, resources, []datapermission.Policy{{
		APIVersion: datapermission.PolicyAPIVersion, Kind: datapermission.PolicyKind,
		Metadata: datapermission.PolicyMetadata{Code: "department-scope", Name: "Department scope"},
		Scope:    datapermission.PolicyBoundary{Type: datapermission.PolicyScopeTenant, TenantID: tenantID},
		Spec:     datapermission.PolicySpec{Resource: "tenant.member", Actions: []string{"read"}, Condition: "resource.department_id in subject.department_ids", Effect: datapermission.EffectAllow},
	}})
	if err != nil {
		t.Fatal(err)
	}
	service := datapermission.NewService(db, engine)
	requestCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: tenantID, MembershipID: membershipID})
	requestCtx = accesscontrol.WithEndpoint(requestCtx, accesscontrol.Endpoint{Resource: "tenant.member", Action: "read", DataPermission: accesscontrol.DataPermissionRequired})
	activeScope, err := service.Compile(requestCtx, "tenant.member")
	if err != nil || len(activeScope.Args) != 1 || activeScope.Args[0] != departmentID {
		t.Fatalf("active department scope=%+v err=%v", activeScope, err)
	}
	if err := appdb.NewTransactor(db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE tenant_departments SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=? AND deleted_at IS NULL`), time.Now(), "system", time.Now(), "system", tenantID, departmentID)
		return err
	}); err != nil {
		t.Fatalf("soft-delete department fixture: %v", err)
	}
	deletedScope, err := service.Compile(requestCtx, "tenant.member")
	if err != nil || deletedScope.Clause != "(1 = 0)" || len(deletedScope.Args) != 0 {
		t.Fatalf("deleted department scope=%+v err=%v", deletedScope, err)
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
		targetMemberID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"
	)
	actorCtx := platformprincipal.SystemContext(ctx, "authorization-integration")
	now := time.Now()
	err := appdb.NewTransactor(db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		statements := []struct {
			query string
			args  []any
		}{
			{
				`INSERT INTO identity_users(id,username,display_name,email,phone,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,1)`,
				[]any{"authorization-admin", "authorization.admin", "Authorization Admin", "", "", "active", now, "authorization-integration", now, "authorization-integration"},
			},
			{
				`INSERT INTO identity_users(id,username,display_name,email,phone,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,1)`,
				[]any{"authorization-target", "authorization.target", "Authorization Target", "", "", "active", now, "authorization-integration", now, "authorization-integration"},
			},
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
	definitions := resourceaction.PlatformResourceDefinitions()
	definitions = append(definitions, resourceaction.ResourceDefinition{Key: "integration.permissions", Name: "Integration permissions", Scope: resourceaction.ResourceScopeTenant, Actions: []resourceaction.ActionDefinition{{Key: "read", Name: "Read"}}})
	registry, registryErr := resourceaction.NewRegistryFromDefinitions(definitions)
	if registryErr != nil {
		t.Fatal(registryErr)
	}
	service := authorization.NewTenantAuthorizationService(db, appdb.NewTransactor(db), nil, discardOperationRecorder{}, discardSecurityRecorder{}, nil, cfg, nil)
	if err := service.SetTenantPermissions(actorCtx, tenantID, 1, []string{permissionID}); err != nil {
		t.Fatalf("set tenant permission ceiling: %v", err)
	}
	if err := service.SetTenantPermissions(actorCtx, tenantID, 1, []string{permissionID}); !errors.Is(err, authorization.ErrTenantAuthorizationStaleVersion) {
		t.Fatalf("stale tenant permission ceiling error=%v", err)
	}
	if err := service.SetAdministrator(actorCtx, tenantID, adminMemberID, true, 1); err != nil {
		t.Fatalf("set tenant administrator: %v", err)
	}
	if err := service.SetAdministrator(actorCtx, tenantID, adminMemberID, false, 1); !errors.Is(err, authorization.ErrTenantAuthorizationStaleVersion) {
		t.Fatalf("stale tenant administrator version error=%v", err)
	}
	if err := service.SetAdministrator(actorCtx, tenantID, adminMemberID, false, 2); !errors.Is(err, authorization.ErrTenantAuthorizationConflict) {
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
	if err := service.SetMemberRoles(adminCtx, targetMemberID, 1, []string{role.ID}); !errors.Is(err, authorization.ErrTenantAuthorizationStaleVersion) {
		t.Fatalf("stale member roles error=%v", err)
	}
	memberRoles, err := service.MemberRoles(adminCtx, targetMemberID)
	if err != nil || len(memberRoles) != 1 || memberRoles[0].Name != "审计员" {
		t.Fatalf("member roles=%+v err=%v", memberRoles, err)
	}
	assignmentTarget, err := service.MemberRoleAssignmentTarget(adminCtx, targetMemberID)
	if err != nil || assignmentTarget.ID != targetMemberID || assignmentTarget.Version != 2 || assignmentTarget.Username != "authorization.target" || len(assignmentTarget.Roles) != 1 || assignmentTarget.Roles[0].ID != role.ID {
		t.Fatalf("member role assignment target=%+v err=%v", assignmentTarget, err)
	}
	targetCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "authorization-target", Type: platformprincipal.TypeUser, TenantID: tenantID, MembershipID: targetMemberID})
	for _, principalCtx := range []context.Context{adminCtx, targetCtx} {
		page, pageErr := service.PageAssignablePermissions(principalCtx, authorization.AssignablePermissionPageInput{
			Request: pagination.Request{Page: 1, PageSize: 20, Keyword: "integration.permissions.read"},
			IDs:     []string{permissionID},
		})
		if pageErr != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != permissionID {
			t.Fatalf("assignable permission page=%+v err=%v", page, pageErr)
		}
	}
	missingPage, err := service.PageAssignablePermissions(targetCtx, authorization.AssignablePermissionPageInput{IDs: []string{"missing-permission"}})
	if err != nil || missingPage.Total != 0 || len(missingPage.Items) != 0 {
		t.Fatalf("missing permission leaked into page=%+v err=%v", missingPage, err)
	}
	effective, err := service.EffectivePermissions(targetCtx, "")
	if err != nil || len(effective) != 1 || effective[0].ID != permissionID || effective[0].Name == "" {
		t.Fatalf("effective permissions=%v err=%v", effective, err)
	}
	page, err := service.PageRoles(adminCtx, authorization.RolePageInput{Request: pagination.Request{Page: 1, PageSize: 20}, Keyword: "审计", IDs: []string{role.ID}, Statuses: []string{"active"}})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("tenant role page=%+v err=%v", page, err)
	}
	otherTenantCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "other", Type: platformprincipal.TypeSystem, TenantID: "other-tenant", MembershipID: "other-member"})
	if _, err := service.MemberRoleAssignmentTarget(otherTenantCtx, targetMemberID); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("cross-tenant member role assignment target error=%v", err)
	}
	if _, err := service.GetRole(otherTenantCtx, role.ID); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("cross-tenant role lookup error=%v", err)
	}
	if _, err := service.RoleUpdateTarget(otherTenantCtx, role.ID); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("cross-tenant role update target error=%v", err)
	}
	if _, err := service.RoleDeleteTarget(otherTenantCtx, role.ID); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("cross-tenant role delete target error=%v", err)
	}
	editTarget, err := service.RoleUpdateTarget(adminCtx, role.ID)
	if err != nil || editTarget.ID != role.ID || editTarget.Description != role.Description || editTarget.Version != role.Version {
		t.Fatalf("role update target=%+v err=%v", editTarget, err)
	}
	deleteTarget, err := service.RoleDeleteTarget(adminCtx, role.ID)
	if err != nil || deleteTarget.ID != role.ID || deleteTarget.Version != role.Version {
		t.Fatalf("role delete target=%+v err=%v", deleteTarget, err)
	}
	testRoleDataPermissionEnforcement(t, ctx, db, registry, tenantID, adminMemberID, &role)
	updated, err := service.UpdateRole(adminCtx, role.ID, "高级审计员", role.Description, "active", role.Version)
	if err != nil || updated.Version != role.Version+1 {
		t.Fatalf("updated tenant role=%+v err=%v", updated, err)
	}
	if _, err := service.UpdateRole(adminCtx, role.ID, "旧版本", role.Description, "active", role.Version); !errors.Is(err, authorization.ErrTenantAuthorizationStaleVersion) {
		t.Fatalf("stale tenant role update error=%v", err)
	}
	if err := service.DeleteRole(adminCtx, role.ID, updated.Version); err != nil {
		t.Fatalf("delete tenant role: %v", err)
	}
	if _, err := service.RoleDeleteTarget(adminCtx, role.ID); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("deleted role action target error=%v", err)
	}
	if _, err := service.GetRole(adminCtx, role.ID); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("deleted tenant role lookup error=%v", err)
	}
	manualPermissions := permission.New(permission.NewRepository(db), appdb.NewTransactor(db), discardOperationRecorder{}, discardSecurityRecorder{}, nil, registry, slog.Default())
	manual, err := manualPermissions.Create(actorCtx, permission.Input{
		Key: "integration.manual.view", Name: "查看手工功能", NodeType: "permission",
		Resource: "manual.feature", Action: "view", Status: "active",
	})
	if err != nil {
		t.Fatalf("create manual resource permission: %v", err)
	}
	if err := service.SetTenantPermissions(actorCtx, tenantID, 2, []string{permissionID, manual.ID}); err != nil {
		t.Fatalf("grant manual resource permission to tenant: %v", err)
	}
	manualPage, err := service.PageAssignablePermissions(adminCtx, authorization.AssignablePermissionPageInput{
		Request: pagination.Request{Page: 1, PageSize: 20}, IDs: []string{manual.ID},
	})
	if err != nil || manualPage.Total != 1 || len(manualPage.Items) != 1 || manualPage.Items[0].ID != manual.ID {
		t.Fatalf("manual resource assignable page=%+v err=%v", manualPage, err)
	}
	manualRole, err := service.CreateRole(adminCtx, "manual_viewer", "手工功能查看者", "", []string{manual.ID})
	if err != nil {
		t.Fatalf("create role with manual resource permission: %v", err)
	}
	manualTarget, err := service.RoleGrantTarget(adminCtx, manualRole.ID)
	if err != nil || len(manualTarget.Permissions) != 1 || !manualTarget.Permissions[0].Assignable {
		t.Fatalf("manual resource role grant target=%+v err=%v", manualTarget, err)
	}
	err = appdb.NewTransactor(db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		_, updateErr := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE authorization_resource_actions SET status='disabled' WHERE id=?`), "manual-action")
		return updateErr
	})
	if err != nil {
		t.Fatalf("disable manual resource action: %v", err)
	}
	manualPage, err = service.PageAssignablePermissions(adminCtx, authorization.AssignablePermissionPageInput{
		Request: pagination.Request{Page: 1, PageSize: 20}, IDs: []string{manual.ID},
	})
	if err != nil || manualPage.Total != 0 || len(manualPage.Items) != 0 {
		t.Fatalf("disabled manual resource leaked into assignable page=%+v err=%v", manualPage, err)
	}
	manualTarget, err = service.RoleGrantTarget(adminCtx, manualRole.ID)
	if err != nil || len(manualTarget.Permissions) != 1 || manualTarget.Permissions[0].Assignable {
		t.Fatalf("disabled manual resource role grant target=%+v err=%v", manualTarget, err)
	}
}

func testRoleDataPermissionEnforcement(t *testing.T, ctx context.Context, db *sqlx.DB, resources *resourceaction.Registry, tenantID, membershipID string, visible *authorization.TenantRole) {
	t.Helper()
	const hiddenID = "5a53dafa-52df-4c51-91bc-475f30712e69"
	now := time.Now()
	actorCtx := platformprincipal.SystemContext(ctx, "other-actor")
	if err := appdb.NewTransactor(db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO tenant_roles(id,tenant_id,code,name,description,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,1)`), hiddenID, tenantID, "hidden-role", "Hidden Role", "", "active", now, "other-actor", now, "other-actor")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	schemas, err := datapermission.NewSchemaRegistry(authorization.NewTenantRoleDataPermissionSchema())
	if err != nil {
		t.Fatal(err)
	}
	authenticated := true
	engine, err := datapermission.NewEngine(schemas, resources, []datapermission.Policy{{
		APIVersion: datapermission.PolicyAPIVersion, Kind: datapermission.PolicyKind,
		Metadata: datapermission.PolicyMetadata{Code: "integration-role-owner", Name: "Integration role owner scope"},
		Scope:    datapermission.PolicyBoundary{Type: datapermission.PolicyScopeTenant, TenantID: tenantID},
		Spec:     datapermission.PolicySpec{Subject: resourceaction.SubjectMatcher{Authenticated: &authenticated}, Resource: "tenant.role", Actions: []string{"read", "list", "update", "delete"}, Condition: "resource.created_by == subject.id", Effect: datapermission.EffectAllow},
	}})
	if err != nil {
		t.Fatal(err)
	}
	service := authorization.NewTenantAuthorizationService(db, appdb.NewTransactor(db), nil, discardOperationRecorder{}, discardSecurityRecorder{}, nil, config.Config{}, datapermission.NewService(db, engine))
	principal := platformprincipal.Principal{ID: "authorization-admin", Type: platformprincipal.TypeUser, TenantID: tenantID, MembershipID: membershipID}
	endpointContext := func(action string) context.Context {
		requestCtx := platformprincipal.WithContext(ctx, principal)
		return accesscontrol.WithEndpoint(requestCtx, accesscontrol.Endpoint{Resource: "tenant.role", Action: action, DataPermission: accesscontrol.DataPermissionRequired})
	}
	if _, err := service.GetRole(endpointContext("read"), hiddenID); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("hidden role get error=%v", err)
	}
	if _, err := service.RoleUpdateTarget(endpointContext("update"), hiddenID); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("hidden role update target error=%v", err)
	}
	if _, err := service.RoleDeleteTarget(endpointContext("delete"), hiddenID); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("hidden role delete target error=%v", err)
	}
	if target, err := service.RoleUpdateTarget(endpointContext("update"), visible.ID); err != nil || target.ID != visible.ID || target.Version != visible.Version {
		t.Fatalf("visible role update target=%+v err=%v", target, err)
	}
	page, err := service.PageRoles(endpointContext("list"), authorization.RolePageInput{Request: pagination.Request{Page: 1, PageSize: 20}})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != visible.ID {
		t.Fatalf("data-scoped role page=%+v err=%v", page, err)
	}
	if _, err := service.UpdateRole(endpointContext("update"), hiddenID, "Denied", "", "active", 1); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("hidden role update error=%v", err)
	}
	updated, err := service.UpdateRole(endpointContext("update"), visible.ID, visible.Name, visible.Description, visible.Status, visible.Version)
	if err != nil {
		t.Fatalf("data-scoped role update: %v", err)
	}
	*visible = updated
	if err := service.DeleteRole(endpointContext("delete"), hiddenID, 1); !errors.Is(err, authorization.ErrTenantAuthorizationNotFound) {
		t.Fatalf("hidden role delete error=%v", err)
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
	if _, err := service.Update(actorCtx, serviceaccount.UpdateInput{ID: current.ID, Name: "Stale", Status: serviceaccount.StatusActive, Version: current.Version}); !errors.Is(err, serviceaccount.ErrStaleVersion) {
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

func TestHistoricalTenantPolicyMigration(t *testing.T) {
	for _, databaseType := range []string{"postgres", "mysql"} {
		t.Run(databaseType, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			dsn, migrationURL := startDatabase(t, ctx, databaseType)
			migrationPath, err := filepath.Abs(filepath.Join("..", "migrations", databaseType))
			if err != nil {
				t.Fatal(err)
			}
			schema := ""
			if databaseType == "postgres" {
				schema = "integration_historical"
			}
			cfg := config.Migration{Path: migrationPath, DatabaseURL: migrationURL, Table: "integration_historical_" + databaseType + "_migrations", Schema: schema, CreateSchema: schema != ""}
			if err := migration.Run(cfg, "up", 41); err != nil {
				t.Fatalf("prepare database at migration 41: %v", err)
			}
			db, err := appdb.Open(ctx, config.Database{Type: databaseType, DSN: dsn, Schema: schema, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute, PingTimeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			testHistoricalTenantPolicyCleanup(t, ctx, db, cfg)
		})
	}
}

func testHistoricalTenantPolicyCleanup(t *testing.T, ctx context.Context, db *sqlx.DB, cfg config.Migration) {
	t.Helper()
	deletedTenantID, liveTenantID, missingTenantID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	type fixture struct {
		name, scope, tenantID string
		ids                   [3]string
		wantActive            bool
	}
	fixtures := []fixture{
		{name: "deleted", scope: "tenant", tenantID: deletedTenantID},
		{name: "missing", scope: "tenant", tenantID: missingTenantID},
		{name: "live", scope: "tenant", tenantID: liveTenantID, wantActive: true},
		{name: "global", scope: "global", wantActive: true},
	}
	actorCtx := platformprincipal.SystemContext(ctx, "integration-policy-migration")
	authenticated := true
	if err := appdb.NewTransactor(db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		for _, tenantRecord := range []struct{ id, code string }{{deletedTenantID, "historical-deleted"}, {liveTenantID, "historical-live"}} {
			if _, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO tenants(id,code,name,description,status,owner_user_id,owner_name,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`), tenantRecord.id, tenantRecord.code, tenantRecord.code, "", "active", "system", "System", now, "integration-policy-migration", now, "integration-policy-migration"); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE tenants SET deleted_at=? WHERE id=? AND deleted_at IS NULL`), now, deletedTenantID); err != nil {
			return err
		}
		for index := range fixtures {
			item := &fixtures[index]
			item.ids = [3]string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
			code := "historical-policy-" + item.name
			document, err := datapermission.MarshalPolicy(datapermission.Policy{
				APIVersion: datapermission.PolicyAPIVersion,
				Kind:       datapermission.PolicyKind,
				Metadata:   datapermission.PolicyMetadata{Code: code, Name: item.name},
				Scope:      datapermission.PolicyBoundary{Type: datapermission.PolicyScopeType(item.scope), TenantID: item.tenantID},
				Spec: datapermission.PolicySpec{
					Subject:   resourceaction.SubjectMatcher{Authenticated: &authenticated},
					Resource:  "tenant.member",
					Actions:   []string{"read"},
					Condition: "resource.owner_id == subject.id",
					Effect:    datapermission.EffectAllow,
				},
			})
			if err != nil {
				return err
			}
			var tenantArg any
			if item.scope == "tenant" {
				tenantArg = item.tenantID
			}
			if _, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO data_permission_policies(id,code,name,scope,tenant_id,published_version_number,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`), item.ids[0], code, item.name, item.scope, tenantArg, 1, "active", now, "integration-policy-migration", now, "integration-policy-migration"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO data_permission_policy_versions(id,policy_id,version_number,document,status,published_at,published_by,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`), item.ids[1], item.ids[0], 1, string(document), "published", now, "integration-policy-migration", now, "integration-policy-migration", now, "integration-policy-migration"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO data_permission_policy_actions(id,policy_version_id,resource,action,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,1)`), item.ids[2], item.ids[1], "tenant.member", "read", now, "integration-policy-migration", now, "integration-policy-migration"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed historical policies: %v", err)
	}
	loaded, err := datapermission.NewRepository(db).LoadAllPublished(ctx)
	if err != nil || len(loaded) != 2 {
		t.Fatalf("published policies before cleanup=%d, want live/global only: %v", len(loaded), err)
	}
	if err := migration.Run(cfg, "up", 1); err != nil {
		t.Fatalf("retire historical policies: %v", err)
	}
	assertPolicies := func() {
		t.Helper()
		for _, item := range fixtures {
			want := 0
			if item.wantActive {
				want = 1
			}
			for index, table := range []string{"data_permission_policies", "data_permission_policy_versions", "data_permission_policy_actions"} {
				var active int
				if err := db.GetContext(ctx, &active, db.Rebind(`SELECT count(*) FROM `+table+` WHERE id=? AND deleted_at IS NULL`), item.ids[index]); err != nil || active != want {
					t.Fatalf("historical %s %s active=%d, want %d: %v", item.name, table, active, want, err)
				}
			}
		}
	}
	assertPolicies()
	var retiredVersion int64
	if err := db.GetContext(ctx, &retiredVersion, db.Rebind(`SELECT version FROM data_permission_policies WHERE id=? AND deleted_by='system:migration'`), fixtures[0].ids[0]); err != nil {
		t.Fatalf("historical policy audit actor: %v", err)
	}
	if err := migration.Run(cfg, "down", -1); err != nil {
		t.Fatalf("repeat historical policy migration down: %v", err)
	}
	if err := migration.Run(cfg, "up", 1); err != nil {
		t.Fatalf("repeat historical policy migration up: %v", err)
	}
	assertPolicies()
	var repeatedVersion int64
	if err := db.GetContext(ctx, &repeatedVersion, db.Rebind(`SELECT version FROM data_permission_policies WHERE id=?`), fixtures[0].ids[0]); err != nil || repeatedVersion != retiredVersion {
		t.Fatalf("repeated cleanup version=%d, want %d: %v", repeatedVersion, retiredVersion, err)
	}
	if err := appdb.NewTransactor(db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		for _, item := range fixtures {
			if !item.wantActive {
				continue
			}
			for _, row := range []struct{ table, id string }{
				{"data_permission_policy_actions", item.ids[2]},
				{"data_permission_policy_versions", item.ids[1]},
				{"data_permission_policies", item.ids[0]},
			} {
				if _, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE `+row.table+` SET deleted_at=? WHERE id=? AND deleted_at IS NULL`), time.Now(), row.id); err != nil {
					return err
				}
			}
		}
		_, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE tenants SET deleted_at=? WHERE id=? AND deleted_at IS NULL`), time.Now(), liveTenantID)
		return err
	}); err != nil {
		t.Fatalf("cleanup historical policy fixtures: %v", err)
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
	securityTarget, err := service.SecurityTarget(actorCtx, created.ID)
	if err != nil || securityTarget.ID != created.ID || securityTarget.Username != created.Username || securityTarget.DisplayName != created.DisplayName || securityTarget.Status != identity.StatusActive {
		t.Fatalf("user security target=%+v err=%v", securityTarget, err)
	}
	deleteTarget, err := service.DeleteTarget(actorCtx, created.ID)
	if err != nil || deleteTarget.ID != created.ID || deleteTarget.Version != created.Version {
		t.Fatalf("user delete target=%+v err=%v", deleteTarget, err)
	}
	updateTarget, err := service.UpdateTarget(actorCtx, created.ID)
	if err != nil || updateTarget.ID != created.ID || updateTarget.Email != created.Email || updateTarget.Version != created.Version {
		t.Fatalf("user update target=%+v err=%v", updateTarget, err)
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
	deleteTarget, err = service.DeleteTarget(actorCtx, created.ID)
	if err != nil || deleteTarget.Version != updated.Version {
		t.Fatalf("updated user delete target=%+v err=%v", deleteTarget, err)
	}
	updateTarget, err = service.UpdateTarget(actorCtx, created.ID)
	if err != nil || updateTarget.Version != updated.Version || updateTarget.DisplayName != updated.DisplayName {
		t.Fatalf("updated user target=%+v err=%v", updateTarget, err)
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
	if err != nil || tokens.AccessToken == "" || tokens.RefreshToken == "" || !tokens.MustChangePassword {
		t.Fatalf("login tokens=%+v err=%v", tokens, err)
	}
	jwtVerifier := auth.NewWithDatabase(authCfg, db)
	verified, tokenState, err := jwtVerifier.VerifyWithState(ctx, tokens.AccessToken)
	if err != nil || verified.ID != created.ID || !tokenState.MustChangePassword {
		t.Fatalf("temporary-password token verification principal=%+v state=%+v err=%v", verified, tokenState, err)
	}
	rotated, err := authenticationService.Refresh(ctx, tokens.RefreshToken)
	if err != nil || rotated.RefreshToken == tokens.RefreshToken || !rotated.MustChangePassword {
		t.Fatalf("refresh tokens=%+v err=%v", rotated, err)
	}
	secondRotation, err := authenticationService.Refresh(ctx, rotated.RefreshToken)
	if err != nil || secondRotation.RefreshToken == rotated.RefreshToken || !secondRotation.MustChangePassword {
		t.Fatalf("second refresh tokens=%+v err=%v", secondRotation, err)
	}
	var historyCount int
	if err := db.GetContext(ctx, &historyCount, db.Rebind(`SELECT count(*) FROM identity_refresh_token_history WHERE session_id=? AND created_by='identity-service:refresh' AND updated_by='identity-service:refresh' AND version=1 AND deleted_at IS NULL`), tokens.SessionID); err != nil || historyCount != 2 {
		t.Fatalf("audited refresh history count=%d err=%v", historyCount, err)
	}
	if _, err := authenticationService.Refresh(ctx, tokens.RefreshToken); !errors.Is(err, userauthentication.ErrRefreshReused) {
		t.Fatalf("two-generation-old refresh replay error=%v", err)
	}
	if _, err := authenticationService.Refresh(ctx, secondRotation.RefreshToken); !errors.Is(err, userauthentication.ErrRefreshInvalid) {
		t.Fatalf("revoked session accepted latest refresh token: %v", err)
	}
	if err := authenticationService.ChangePassword(creatorCtx, "correct horse battery staple", "a different correct horse battery staple"); err != nil {
		t.Fatalf("change temporary password: %v", err)
	}
	if _, err := jwtVerifier.Verify(ctx, tokens.AccessToken); err == nil {
		t.Fatal("password change did not revoke the temporary-password access token")
	}
	unrestricted, err := authenticationService.Login(ctx, "alice.smith", "a different correct horse battery staple", "127.0.0.1", "integration-test")
	if err != nil || unrestricted.MustChangePassword {
		t.Fatalf("unrestricted login tokens=%+v err=%v", unrestricted, err)
	}
	verified, tokenState, err = jwtVerifier.VerifyWithState(ctx, unrestricted.AccessToken)
	if err != nil || verified.ID != created.ID || tokenState.MustChangePassword {
		t.Fatalf("changed-password token verification principal=%+v state=%+v err=%v", verified, tokenState, err)
	}
	if _, err := authenticationService.Refresh(ctx, "never-issued-refresh-token"); !errors.Is(err, userauthentication.ErrRefreshInvalid) {
		t.Fatalf("unknown refresh token error=%v", err)
	}
	if _, err := jwtVerifier.Verify(ctx, unrestricted.AccessToken); err != nil {
		t.Fatalf("unknown token revoked another session: %v", err)
	}
	revokeTokens, err := authenticationService.Login(ctx, "alice.smith", "a different correct horse battery staple", "203.0.113.1", "integration-revoke-target")
	if err != nil {
		t.Fatalf("create revoke-target session: %v", err)
	}
	target, err := authenticationService.SessionRevokeTarget(creatorCtx, revokeTokens.SessionID)
	if err != nil || target.Status != userauthentication.SessionStatusActive || target.Version < 1 || target.ClientIP != "203.0.113.1" {
		t.Fatalf("own revoke target=%+v err=%v", target, err)
	}
	if _, err := authenticationService.SessionRevokeTarget(actorCtx, revokeTokens.SessionID); !errors.Is(err, userauthentication.ErrSessionNotFound) {
		t.Fatalf("another user's session must be hidden: %v", err)
	}
	if err := authenticationService.RevokeSession(creatorCtx, revokeTokens.SessionID, target.Version+1); !errors.Is(err, userauthentication.ErrSessionVersionConflict) {
		t.Fatalf("stale revoke version must conflict: %v", err)
	}
	if err := authenticationService.RevokeSession(creatorCtx, revokeTokens.SessionID, target.Version); err != nil {
		t.Fatalf("revoke own session: %v", err)
	}
	revokedTarget, err := authenticationService.SessionRevokeTarget(creatorCtx, revokeTokens.SessionID)
	if err != nil || revokedTarget.Status != userauthentication.SessionStatusRevoked || revokedTarget.Version <= target.Version {
		t.Fatalf("revoked target=%+v err=%v", revokedTarget, err)
	}
	if err := authenticationService.Logout(ctx, unrestricted.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(actorCtx, created.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	store := cache.NewRedisStore(redisClient)
	for _, key := range []string{"identity:user:v1:id:" + created.ID, "identity:user:v1:username:" + created.Username} {
		if err := cache.SetJSON(ctx, store, key, updated, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	cachedService := identity.New(identity.NewRepository(db), nil, store, nil, nil, slog.Default(), config.Config{User: config.User{CacheTTL: time.Minute}})
	if _, err := cachedService.Get(actorCtx, created.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("deleted user must not be returned by stale ID cache: %v", err)
	}
	if _, err := cachedService.ResolveUsername(ctx, created.Username); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("deleted user must not be returned by stale username cache: %v", err)
	}
	if _, err := service.SecurityTarget(actorCtx, created.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("deleted user must not be an actionable security target: %v", err)
	}
	if _, err := service.DeleteTarget(actorCtx, created.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("deleted user must not be a delete target: %v", err)
	}
	if _, err := service.UpdateTarget(actorCtx, created.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("deleted user must not be an update target: %v", err)
	}
	if err := authenticationService.ForceLogoutAll(actorCtx, created.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("deleted user must not be forcibly logged out as a successful action: %v", err)
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
	definitions := resourceaction.PlatformResourceDefinitions()
	definitions = append(definitions, resourceaction.ResourceDefinition{Key: "integration.permissions", Name: "Integration permissions", Scope: resourceaction.ResourceScopeTenant, Actions: []resourceaction.ActionDefinition{{Key: "read", Name: "Read"}}})
	resources, err := resourceaction.NewRegistryFromDefinitions(definitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := resourcecatalog.New(db, appdb.NewTransactor(db), resources).Sync(ctx, "2026-10-13T08:03:00Z", "permission-integration"); err != nil {
		t.Fatalf("sync permission resource catalog: %v", err)
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
	if leaf.ParentName != group.Name || group.ParentName != "" {
		t.Fatalf("permission parent presentation: group=%+v leaf=%+v", group, leaf)
	}
	groupTarget, err := service.NodeUpdateTarget(actorCtx, group.ID)
	if err != nil || groupTarget.ID != group.ID || !groupTarget.HasChildren || groupTarget.Version != group.Version {
		t.Fatalf("permission group update target=%+v err=%v", groupTarget, err)
	}
	leafTarget, err := service.NodeDeleteTarget(actorCtx, leaf.ID)
	if err != nil || leafTarget.ID != leaf.ID || leafTarget.ParentName != group.Name || leafTarget.Version != leaf.Version {
		t.Fatalf("permission leaf delete target=%+v err=%v", leafTarget, err)
	}
	if _, err := service.Update(actorCtx, group.ID, group.Version+1, permission.Input{Key: group.Key, Name: group.Name, NodeType: "group", Status: "active"}); !errors.Is(err, permission.ErrStaleVersion) {
		t.Fatalf("permission stale update error=%v", err)
	}
	if err := service.Delete(actorCtx, leaf.ID, leaf.Version+1); !errors.Is(err, permission.ErrStaleVersion) {
		t.Fatalf("permission stale delete error=%v", err)
	}
	tree, err := service.Tree(actorCtx, permission.TreeInput{Keyword: "查询集成权限", NodeTypes: []string{"permission"}, Statuses: []string{"active"}})
	if err != nil || len(tree) != 1 || len(tree[0].Children) != 1 || tree[0].Children[0].ParentName != group.Name {
		t.Fatalf("filtered permission tree=%+v err=%v", tree, err)
	}
	convert := permission.Input{Key: group.Key, Name: group.Name, NodeType: "permission", Resource: "integration.permissions", Action: "read", Status: "active"}
	if _, err := service.Update(actorCtx, group.ID, group.Version, convert); !errors.Is(err, permission.ErrInvalid) {
		t.Fatalf("convert group with child to permission error=%v", err)
	}
	unchanged, err := service.Get(actorCtx, group.ID)
	if err != nil || unchanged.NodeType != "group" || unchanged.Version != group.Version {
		t.Fatalf("group changed after rejected conversion=%+v err=%v", unchanged, err)
	}
	leafGroup, err := service.Create(actorCtx, permission.Input{ParentID: &group.ID, Key: "integration.permissions.leaf_group", Name: "空分组", NodeType: "group", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	converted, err := service.Update(actorCtx, leafGroup.ID, leafGroup.Version, permission.Input{ParentID: &group.ID, Key: leafGroup.Key, Name: leafGroup.Name, NodeType: "permission", Resource: "integration.permissions", Action: "read", Status: "active"})
	if err != nil || converted.NodeType != "permission" || converted.Version != leafGroup.Version+1 {
		t.Fatalf("convert leaf group to permission=%+v err=%v", converted, err)
	}

	return leaf.ID
}

func testManagedAuthorizationLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB, tenantPermissionID string) {
	t.Helper()
	actorCtx := platformprincipal.SystemContext(ctx, "authorization-integration")
	transactor := appdb.NewTransactor(db)
	catalog := resourcecatalog.NewService(db, transactor, discardOperationRecorder{}, discardSecurityRecorder{})
	resource, err := catalog.CreateResource(actorCtx, resourcecatalog.ResourceInput{Key: "integration.manual", Name: "Catalog Display", Scope: resourceaction.ResourceScopePlatform, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	action, err := catalog.CreateAction(actorCtx, resourcecatalog.ActionInput{ResourceID: resource.ID, Key: "read", Name: "Read", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if action.ResourceName != resource.Name {
		t.Fatalf("created action resource name=%q, want %q", action.ResourceName, resource.Name)
	}
	page, err := catalog.PageResources(actorCtx, resourcecatalog.PageInput{Request: pagination.Request{Page: 1, PageSize: 20, Keyword: "Catalog Display"}, Statuses: []string{"active"}})
	if err != nil || page.Total != 1 || page.Items[0].ID != resource.ID {
		t.Fatalf("resource page=%+v err=%v", page, err)
	}
	actionPage, err := catalog.PageActions(actorCtx, resourcecatalog.PageInput{Request: pagination.Request{Page: 1, PageSize: 20}, ResourceID: resource.ID})
	if err != nil || actionPage.Total != 1 || actionPage.Items[0].ResourceKey != "integration.manual" || actionPage.Items[0].ResourceName != resource.Name {
		t.Fatalf("action page=%+v err=%v", actionPage, err)
	}
	for _, keyword := range []string{"integration.manual", "Catalog Display"} {
		searched, searchErr := catalog.PageActions(actorCtx, resourcecatalog.PageInput{
			Request:    pagination.Request{Page: 1, PageSize: 20, Keyword: keyword},
			ResourceID: resource.ID,
			Statuses:   []string{"active"},
		})
		if searchErr != nil || searched.Total != 1 || len(searched.Items) != 1 || searched.Items[0].ID != action.ID {
			t.Fatalf("action search keyword=%q page=%+v err=%v", keyword, searched, searchErr)
		}
	}
	filteredActions, err := catalog.PageActions(actorCtx, resourcecatalog.PageInput{Request: pagination.Request{Page: 1, PageSize: 20}, ResourceKey: "integration.manual", Scope: resourceaction.ResourceScopePlatform, Statuses: []string{"active"}, ActiveResourcesOnly: true})
	if err != nil || filteredActions.Total != 1 || filteredActions.Items[0].ID != action.ID {
		t.Fatalf("scope-filtered action page=%+v err=%v", filteredActions, err)
	}
	otherScope, err := catalog.PageActions(actorCtx, resourcecatalog.PageInput{Request: pagination.Request{Page: 1, PageSize: 20}, ResourceKey: "integration.manual", Scope: resourceaction.ResourceScopeTenant, ActiveResourcesOnly: true})
	if err != nil || otherScope.Total != 0 {
		t.Fatalf("other-scope action page=%+v err=%v", otherScope, err)
	}
	action, err = catalog.UpdateAction(actorCtx, action.ID, action.Version, resourcecatalog.ActionInput{ResourceID: resource.ID, Key: "read", Name: "View", Status: "active"})
	if err != nil || action.Version != 2 {
		t.Fatalf("action update=%+v err=%v", action, err)
	}
	if err := catalog.DeleteAction(actorCtx, action.ID, action.Version); err != nil {
		t.Fatal(err)
	}
	resource, err = catalog.UpdateResource(actorCtx, resource.ID, resource.Version, resourcecatalog.ResourceInput{Key: "integration.manual", Name: "Manual Updated", Scope: resourceaction.ResourceScopePlatform, Status: "active"})
	if err != nil || resource.Version != 2 {
		t.Fatalf("resource update=%+v err=%v", resource, err)
	}
	if err := catalog.DeleteResource(actorCtx, resource.ID, resource.Version); err != nil {
		t.Fatal(err)
	}
	definitions := resourceaction.PlatformResourceDefinitions()
	registry, err := resourceaction.NewRegistryFromDefinitions(definitions)
	if err != nil {
		t.Fatal(err)
	}
	permissions := permission.New(permission.NewRepository(db), transactor, discardOperationRecorder{}, discardSecurityRecorder{}, nil, registry, slog.Default())
	grantPermission, err := permissions.Create(actorCtx, permission.Input{Key: "integration.platform.catalog.read", Name: "Read catalog", NodeType: "permission", Resource: "authorization.resource", Action: "read", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	var secondActionID string
	if err := db.GetContext(ctx, &secondActionID, db.Rebind(`SELECT a.id FROM authorization_resource_actions a JOIN authorization_resources r ON r.id=a.resource_id WHERE r.resource_key=? AND a.action_key=?`), "authorization.action", "read"); err != nil {
		t.Fatal(err)
	}
	links, err := permissions.GetActions(actorCtx, grantPermission.ID)
	if err != nil || len(links) != 1 {
		t.Fatalf("permission links=%+v err=%v", links, err)
	}
	grantPermission, err = permissions.SetActions(actorCtx, grantPermission.ID, grantPermission.Version, []string{links[0].ID, secondActionID})
	if err != nil || grantPermission.Version != 2 {
		t.Fatalf("set permission links=%+v err=%v", grantPermission, err)
	}
	links, err = permissions.GetActions(actorCtx, grantPermission.ID)
	if err != nil || len(links) != 2 {
		t.Fatalf("multiple permission links=%+v err=%v", links, err)
	}
	users := identity.New(identity.NewRepository(db), transactor, nil, discardOperationRecorder{}, discardSecurityRecorder{}, slog.Default(), config.Config{User: config.User{CacheTTL: time.Minute}})
	user, err := users.Create(actorCtx, identity.CreateInput{Username: "grant.integration", DisplayName: "Grant Test"})
	if err != nil {
		t.Fatal(err)
	}
	principal := platformprincipal.Principal{ID: user.ID, Type: platformprincipal.TypeUser}
	authorizer := authorization.New(db, registry)
	snapshotter, ok := authorizer.(interface {
		Snapshot(context.Context, platformprincipal.Principal) (context.Context, string, error)
	})
	if !ok {
		t.Fatal("authorizer does not support grant snapshots")
	}
	_, revisionBeforeGrant, err := snapshotter.Snapshot(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	grants := authorization.NewPlatformGrantService(db, transactor, discardOperationRecorder{}, discardSecurityRecorder{})
	platformOptions, err := grants.PageGrantablePermissions(actorCtx, authorization.PlatformGrantablePermissionPageInput{Request: pagination.Request{Keyword: "integration.platform.catalog.read"}})
	if err != nil || platformOptions.Total != 1 || len(platformOptions.Items) != 1 || platformOptions.Items[0].ID != grantPermission.ID {
		t.Fatalf("platform permission options=%+v err=%v", platformOptions, err)
	}
	tenantOptions, err := grants.PageGrantablePermissions(actorCtx, authorization.PlatformGrantablePermissionPageInput{Request: pagination.Request{Keyword: "integration.permissions.read"}})
	if err != nil || tenantOptions.Total != 0 || len(tenantOptions.Items) != 0 {
		t.Fatalf("tenant permission leaked into platform options=%+v err=%v", tenantOptions, err)
	}
	tenantCeilingService := authorization.NewTenantAuthorizationService(db, transactor, nil, nil, nil, nil, config.Config{}, nil)
	tenantCeilingOptions, err := tenantCeilingService.PageTenantGrantablePermissions(actorCtx, pagination.Request{Keyword: "integration.permissions.read"})
	if err != nil || tenantCeilingOptions.Total != 1 || len(tenantCeilingOptions.Items) != 1 || tenantCeilingOptions.Items[0].ID != tenantPermissionID {
		t.Fatalf("tenant ceiling options=%+v err=%v", tenantCeilingOptions, err)
	}
	platformCeilingOptions, err := tenantCeilingService.PageTenantGrantablePermissions(actorCtx, pagination.Request{Keyword: "integration.platform.catalog.read"})
	if err != nil || platformCeilingOptions.Total != 0 || len(platformCeilingOptions.Items) != 0 {
		t.Fatalf("platform permission leaked into tenant ceiling options=%+v err=%v", platformCeilingOptions, err)
	}
	grant, err := grants.Create(actorCtx, authorization.PlatformGrantInput{PrincipalType: "user", PrincipalID: user.ID, PermissionID: grantPermission.ID})
	if err != nil || grant.PrincipalName != "Grant Test" || grant.PrincipalIdentifier != "grant.integration" {
		t.Fatalf("platform grant=%+v err=%v", grant, err)
	}
	snapshotCtx, revisionAfterGrant, err := snapshotter.Snapshot(ctx, principal)
	if err != nil || revisionAfterGrant == revisionBeforeGrant {
		t.Fatalf("grant revision did not change: before=%q after=%q err=%v", revisionBeforeGrant, revisionAfterGrant, err)
	}
	if err := authorizer.Authorize(snapshotCtx, principal, platformauthz.Requirement{Resource: "authorization.resource", Action: "read", Scope: platformauthz.ScopePlatform}); err != nil {
		t.Fatalf("snapshot did not permit granted action: %v", err)
	}
	if err := authorizer.Authorize(ctx, principal, platformauthz.Requirement{Resource: "authorization.resource", Action: "read", Scope: platformauthz.ScopePlatform}); err != nil {
		t.Fatalf("platform grant denied: %v", err)
	}
	grantPage, err := grants.Page(actorCtx, authorization.PlatformGrantPageInput{Request: pagination.Request{Page: 1, PageSize: 20}, PrincipalID: user.ID})
	if err != nil || grantPage.Total != 1 || len(grantPage.Items) != 1 || grantPage.Items[0].PrincipalIdentifier != "grant.integration" {
		t.Fatalf("platform grant page=%+v err=%v", grantPage, err)
	}
	updateTarget, err := grants.UpdateTarget(actorCtx, grant.ID)
	if err != nil || updateTarget.ID != grant.ID || updateTarget.PrincipalID != user.ID || updateTarget.PermissionID != grantPermission.ID || updateTarget.Version != grant.Version {
		t.Fatalf("platform grant update target=%+v err=%v", updateTarget, err)
	}
	deleteTarget, err := grants.DeleteTarget(actorCtx, grant.ID)
	if err != nil || deleteTarget.ID != grant.ID || deleteTarget.PrincipalIdentifier != "grant.integration" || deleteTarget.PermissionName != grant.PermissionName || deleteTarget.Version != grant.Version {
		t.Fatalf("platform grant delete target=%+v err=%v", deleteTarget, err)
	}
	grant, err = grants.Update(actorCtx, grant.ID, grant.Version, authorization.PlatformGrantInput{PrincipalType: "user", PrincipalID: user.ID, PermissionID: grantPermission.ID})
	if err != nil || grant.Version != 2 {
		t.Fatalf("platform grant update=%+v err=%v", grant, err)
	}
	if err := grants.Delete(actorCtx, grant.ID, grant.Version); err != nil {
		t.Fatal(err)
	}
	_, revisionAfterDelete, err := snapshotter.Snapshot(ctx, principal)
	if err != nil || revisionAfterDelete != revisionBeforeGrant {
		t.Fatalf("grant revision did not recover after delete: before=%q after=%q err=%v", revisionBeforeGrant, revisionAfterDelete, err)
	}
	if _, err := grants.UpdateTarget(actorCtx, grant.ID); !errors.Is(err, authorization.ErrPlatformGrantNotFound) {
		t.Fatalf("deleted platform grant update target err=%v", err)
	}
	if _, err := grants.DeleteTarget(actorCtx, grant.ID); !errors.Is(err, authorization.ErrPlatformGrantNotFound) {
		t.Fatalf("deleted platform grant delete target err=%v", err)
	}
	if err := authorizer.Authorize(ctx, principal, platformauthz.Requirement{Resource: "authorization.resource", Action: "read", Scope: platformauthz.ScopePlatform}); !errors.Is(err, platformauthz.ErrDenied) {
		t.Fatalf("revoked platform grant authorized: %v", err)
	}
	regrant, err := grants.Create(actorCtx, authorization.PlatformGrantInput{PrincipalType: "user", PrincipalID: user.ID, PermissionID: grantPermission.ID})
	if err != nil || regrant.ID == grant.ID {
		t.Fatalf("regrant after logical delete=%+v err=%v", regrant, err)
	}
	if err := authorizer.Authorize(ctx, principal, platformauthz.Requirement{Resource: "authorization.resource", Action: "read", Scope: platformauthz.ScopePlatform}); err != nil {
		t.Fatalf("regrant not effective: %v", err)
	}
}

func testApplicationNavigationLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	definitions := resourceaction.PlatformResourceDefinitions()
	definitions = append(definitions, resourceaction.ResourceDefinition{Key: "integration.permissions", Name: "Integration permissions", Scope: resourceaction.ResourceScopeTenant, Actions: []resourceaction.ActionDefinition{{Key: "read", Name: "Read"}}})
	resources, err := resourceaction.NewRegistryFromDefinitions(definitions)
	if err != nil {
		t.Fatal(err)
	}
	transactor := appdb.NewTransactor(db)
	applications := application.New(db, transactor, nil, nil)
	navigations := navigation.New(db, transactor, resources, nil, nil, nil, slog.Default(), config.Config{Navigation: config.Navigation{CacheTTL: time.Minute}})
	actorCtx := platformprincipal.SystemContext(ctx, "navigation-integration")
	app, err := applications.Create(actorCtx, application.Input{Code: "integration", Name: "集成应用", HomePath: "/integration", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	deleteTarget, err := applications.DeleteTarget(actorCtx, app.ID)
	if err != nil || deleteTarget.ID != app.ID || deleteTarget.Code != app.Code || deleteTarget.Version != app.Version {
		t.Fatalf("application delete target=%+v err=%v", deleteTarget, err)
	}
	updateTarget, err := applications.UpdateTarget(actorCtx, app.ID)
	if err != nil || updateTarget.ID != app.ID || updateTarget.Name != app.Name || updateTarget.Version != app.Version {
		t.Fatalf("application update target=%+v err=%v", updateTarget, err)
	}
	updatedApplication, err := applications.Update(actorCtx, application.UpdateInput{ID: app.ID, Name: "集成应用更新", HomePath: app.HomePath, Status: app.Status, Version: app.Version})
	if err != nil || updatedApplication.Version != app.Version+1 {
		t.Fatalf("updated application=%+v err=%v", updatedApplication, err)
	}
	deleteTarget, err = applications.DeleteTarget(actorCtx, app.ID)
	if err != nil || deleteTarget.Version != updatedApplication.Version || deleteTarget.Name != updatedApplication.Name {
		t.Fatalf("updated application delete target=%+v err=%v", deleteTarget, err)
	}
	updateTarget, err = applications.UpdateTarget(actorCtx, app.ID)
	if err != nil || updateTarget.Version != updatedApplication.Version || updateTarget.Name != updatedApplication.Name {
		t.Fatalf("updated application update target=%+v err=%v", updateTarget, err)
	}
	access := application.NewTenantAccessService(db, transactor, discardOperationRecorder{}, discardSecurityRecorder{}, nil)
	grant, err := access.Grant(actorCtx, application.GrantInput{TenantID: "authorization-tenant", ApplicationID: app.ID})
	if err != nil || grant.Status != "active" || grant.Version != 1 {
		t.Fatalf("tenant application grant=%+v err=%v", grant, err)
	}
	currentCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "authorization-admin", Type: platformprincipal.TypeUser, SessionID: "application-context-session", TenantID: "authorization-tenant", MembershipID: "authorization-admin-member"})
	if err := transactor.Within(currentCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		_, err := tx.ExecContext(currentCtx, tx.Rebind(`INSERT INTO identity_sessions(id,user_id,refresh_token_hash,previous_refresh_token_hash,expires_at,last_seen_at,revoke_reason,client_ip,user_agent,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1)`), "application-context-session", "authorization-admin", "application-context-refresh-token-hash", "", now.Add(time.Hour), now, "", "127.0.0.1", "integration", now, "authorization-admin", now, "authorization-admin")
		return err
	}); err != nil {
		t.Fatalf("create application context session: %v", err)
	}
	currentApplications, err := access.Current(currentCtx)
	if err != nil || len(currentApplications) != 1 || currentApplications[0].ID != app.ID {
		t.Fatalf("current applications=%+v err=%v", currentApplications, err)
	}
	selected, err := access.SwitchContext(currentCtx, application.SwitchInput{ApplicationID: app.ID})
	if err != nil || selected.ApplicationID != app.ID || selected.Version != 1 {
		t.Fatalf("selected application=%+v err=%v", selected, err)
	}
	selected, err = access.SwitchContext(currentCtx, application.SwitchInput{ApplicationID: app.ID, Version: selected.Version})
	if err != nil || selected.Version != 2 {
		t.Fatalf("updated selected application=%+v err=%v", selected, err)
	}
	setTenantStatus := func(status string) {
		t.Helper()
		if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE tenants SET status=? WHERE id=? AND deleted_at IS NULL`), status, "authorization-tenant")
			return err
		}); err != nil {
			t.Fatalf("set application-context tenant status to %s: %v", status, err)
		}
	}
	setTenantStatus("disabled")
	if currentApplications, err := access.Current(currentCtx); err != nil || len(currentApplications) != 0 {
		t.Fatalf("disabled tenant application list=%+v err=%v", currentApplications, err)
	}
	if _, err := access.CurrentContext(currentCtx); !errors.Is(err, application.ErrContextNotFound) {
		t.Fatalf("disabled tenant application context error=%v", err)
	}
	if _, err := access.SwitchContext(currentCtx, application.SwitchInput{ApplicationID: app.ID, Version: selected.Version}); !errors.Is(err, application.ErrGrantForbidden) {
		t.Fatalf("switch application for disabled tenant error=%v", err)
	}
	setTenantStatus("active")
	root, err := navigations.Create(actorCtx, navigation.Input{ApplicationID: app.ID, Key: "system", Name: "系统", Type: "directory", Visible: true, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	setTenantStatus("disabled")
	if tree, err := navigations.CurrentTree(currentCtx, app.ID); err != nil || len(tree) != 0 {
		t.Fatalf("disabled tenant navigation tree=%+v err=%v", tree, err)
	}
	setTenantStatus("active")
	if root.ParentName != "" {
		t.Fatalf("root navigation parent name=%q, want empty", root.ParentName)
	}
	page, err := navigations.Create(actorCtx, navigation.Input{ApplicationID: app.ID, ParentID: &root.ID, Key: "permissions", Name: "权限", Type: "menu", RoutePath: "/permissions", Component: "system/permissions", Resource: "integration.permissions", Action: "read", Visible: true, Status: "active"})
	if err != nil || page.ParentName != "系统" {
		t.Fatalf("created navigation parent name=%q err=%v", page.ParentName, err)
	}
	navigationUpdateTarget, err := navigations.UpdateTarget(actorCtx, page.ID)
	if err != nil || navigationUpdateTarget.ID != page.ID || navigationUpdateTarget.ApplicationName != updatedApplication.Name || navigationUpdateTarget.ParentName != root.Name || navigationUpdateTarget.Version != page.Version {
		t.Fatalf("navigation update target=%+v err=%v", navigationUpdateTarget, err)
	}
	navigationDeleteTarget, err := navigations.DeleteTarget(actorCtx, page.ID)
	if err != nil || navigationDeleteTarget.ID != page.ID || navigationDeleteTarget.ApplicationName != updatedApplication.Name || navigationDeleteTarget.Version != page.Version {
		t.Fatalf("navigation delete target=%+v err=%v", navigationDeleteTarget, err)
	}
	currentTree, err := navigations.CurrentTree(currentCtx, app.ID)
	if err != nil || len(currentTree) != 1 || len(currentTree[0].Children) != 1 || currentTree[0].Children[0].ParentName != "系统" {
		t.Fatalf("current navigation tree=%+v err=%v", currentTree, err)
	}
	tree, err := navigations.Tree(actorCtx, navigation.TreeInput{ApplicationID: app.ID, Keyword: "权限", Types: []string{"menu"}})
	if err != nil || len(tree) != 1 || len(tree[0].Children) != 1 || tree[0].Children[0].ParentName != "系统" {
		t.Fatalf("filtered navigation tree=%+v err=%v", tree, err)
	}
	updated, err := navigations.Update(actorCtx, navigation.UpdateInput{ID: page.ID, ParentID: page.ParentID, Name: "权限管理", Type: page.Type, RoutePath: page.RoutePath, Component: page.Component, Resource: page.Resource, Action: page.Action, Visible: page.Visible, Status: page.Status, Version: page.Version})
	if err != nil || updated.Version != page.Version+1 || updated.ParentName != "系统" {
		t.Fatalf("updated navigation=%+v err=%v", updated, err)
	}
	if _, err := navigations.Update(actorCtx, navigation.UpdateInput{ID: page.ID, ParentID: page.ParentID, Name: "旧版本", Type: page.Type, RoutePath: page.RoutePath, Component: page.Component, Resource: page.Resource, Action: page.Action, Visible: page.Visible, Status: page.Status, Version: page.Version}); !errors.Is(err, navigation.ErrStaleVersion) {
		t.Fatalf("stale navigation update error=%v", err)
	}
	navigationDeleteTarget, err = navigations.DeleteTarget(actorCtx, page.ID)
	if err != nil || navigationDeleteTarget.Version != updated.Version || navigationDeleteTarget.Name != updated.Name {
		t.Fatalf("updated navigation delete target=%+v err=%v", navigationDeleteTarget, err)
	}
	if err := navigations.Delete(actorCtx, root.ID, root.Version); !errors.Is(err, navigation.ErrConflict) {
		t.Fatalf("delete navigation with child error=%v", err)
	}
	if err := navigations.Delete(actorCtx, page.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := navigations.DeleteTarget(actorCtx, page.ID); !errors.Is(err, navigation.ErrNotFound) {
		t.Fatalf("deleted navigation target error=%v", err)
	}
	if err := navigations.Delete(actorCtx, root.ID, root.Version); err != nil {
		t.Fatal(err)
	}
	if err := access.Revoke(actorCtx, "authorization-tenant", grant.ID, grant.Version); err != nil {
		t.Fatal(err)
	}
	currentApplications, err = access.Current(currentCtx)
	if err != nil || len(currentApplications) != 0 {
		t.Fatalf("applications after revoke=%+v err=%v", currentApplications, err)
	}
	if _, err := access.CurrentContext(currentCtx); !errors.Is(err, application.ErrContextNotFound) {
		t.Fatalf("current context after revoke error=%v", err)
	}
	if err := applications.Delete(actorCtx, app.ID, updatedApplication.Version); err != nil {
		t.Fatalf("delete application after revoke error=%v", err)
	}
	if _, err := applications.DeleteTarget(actorCtx, app.ID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("deleted application must not be an action target: %v", err)
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
	updateTarget, err := service.UpdateTarget(actorCtx, created.ID)
	if err != nil || updateTarget.ID != created.ID || updateTarget.Key != created.Key || updateTarget.Version != created.Version {
		t.Fatalf("platform config update target=%+v err=%v", updateTarget, err)
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
	if _, err := service.Update(actorCtx, platformconfig.UpdateInput{ID: created.ID, Name: created.Name, Category: created.Category, Value: []byte(`true`), Status: created.Status, Version: created.Version}); !errors.Is(err, platformconfig.ErrStaleVersion) {
		t.Fatalf("stale platform config update error=%v", err)
	}
	deleteTarget, err := service.DeleteTarget(actorCtx, updated.ID)
	if err != nil || deleteTarget.ID != updated.ID || deleteTarget.Key != updated.Key || deleteTarget.IsPublic || deleteTarget.Version != updated.Version {
		t.Fatalf("platform config delete target=%+v err=%v", deleteTarget, err)
	}
	if _, err := service.GetPublic(ctx, created.Key); !errors.Is(err, platformconfig.ErrNotFound) {
		t.Fatalf("private config public lookup error=%v", err)
	}
	if err := service.Delete(actorCtx, created.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetPublic(ctx, created.Key); !errors.Is(err, platformconfig.ErrNotFound) {
		t.Fatalf("deleted config public lookup error=%v", err)
	}
	if _, err := service.DeleteTarget(actorCtx, created.ID); !errors.Is(err, platformconfig.ErrNotFound) {
		t.Fatalf("deleted platform config action target error=%v", err)
	}
	restored, err := service.Create(actorCtx, platformconfig.Input{Key: created.Key, Name: "恢复配置", Category: created.Category, Value: []byte(`true`), IsPublic: true, Status: "active"})
	if err != nil || restored.ID != created.ID || restored.Version <= updated.Version {
		t.Fatalf("restored stable platform config=%+v err=%v", restored, err)
	}
	if err := service.Delete(actorCtx, restored.ID, restored.Version); err != nil {
		t.Fatal(err)
	}
}

func testScheduledJobLifecycle(t *testing.T, ctx context.Context, db *sqlx.DB) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transactor := appdb.NewTransactor(db)
	handlers := scheduler.NewHandlerRegistry(logger)
	cfg := config.Config{
		App:  config.App{Name: "scheduler-integration"},
		Cron: config.Cron{Timezone: "Asia/Shanghai"},
	}
	runtime, err := scheduler.NewManager(
		integrationLifecycle{},
		cfg,
		db,
		transactor,
		integrationLifecycleLocker{},
		observability.NewMetrics(cfg, nil, nil),
		logger,
		handlers,
		discardOperationRecorder{},
	)
	if err != nil {
		t.Fatalf("create scheduler runtime: %v", err)
	}
	service := scheduler.NewDefinitionService(db, transactor, discardOperationRecorder{}, nil, handlers, runtime, nil)
	actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "scheduler-admin", Type: platformprincipal.TypeUser})

	created, err := service.Create(actorCtx, scheduler.DefinitionInput{
		Code:           "integration-sample",
		Name:           "Integration sample",
		CronSpec:       "0 */5 * * * *",
		Timezone:       "Asia/Shanghai",
		Handler:        "system.sample",
		TimeoutSeconds: 30,
		LockTTLSeconds: 60,
		Status:         "active",
	})
	if err != nil {
		t.Fatalf("create scheduled job: %v", err)
	}
	if created.Version != 1 || created.CreatedBy != "scheduler-admin" || created.UpdatedBy != "scheduler-admin" {
		t.Fatalf("created scheduled job audit = %+v", created)
	}
	updateTarget, err := service.UpdateTarget(actorCtx, created.ID)
	if err != nil || updateTarget.ID != created.ID || updateTarget.Code != created.Code || updateTarget.Version != created.Version {
		t.Fatalf("scheduled job update target = %+v err=%v", updateTarget, err)
	}

	page, err := service.Page(actorCtx, scheduler.DefinitionPageInput{
		Request:  pagination.Request{Page: 1, PageSize: 20, Keyword: "integration"},
		Statuses: []string{"active"},
	})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != created.ID {
		t.Fatalf("page scheduled jobs = %+v err=%v", page, err)
	}

	update := scheduler.UpdateDefinitionInput{
		ID:             created.ID,
		Name:           "Updated integration sample",
		CronSpec:       created.CronSpec,
		Timezone:       created.Timezone,
		Handler:        created.Handler,
		TimeoutSeconds: created.TimeoutSeconds,
		LockTTLSeconds: created.LockTTLSeconds,
		Status:         created.Status,
		Payload:        created.Payload,
		Version:        created.Version,
	}
	updated, err := service.Update(actorCtx, update)
	if err != nil {
		t.Fatalf("update scheduled job: %v", err)
	}
	if updated.Version != 2 || updated.Name != update.Name {
		t.Fatalf("updated scheduled job = %+v", updated)
	}
	if _, err := service.Update(actorCtx, update); !errors.Is(err, scheduler.ErrDefinitionStaleVersion) {
		t.Fatalf("stale scheduled job update error = %v", err)
	}
	deleteTarget, err := service.DeleteTarget(actorCtx, updated.ID)
	if err != nil || deleteTarget.ID != updated.ID || deleteTarget.Name != updated.Name || deleteTarget.Version != updated.Version {
		t.Fatalf("scheduled job delete target = %+v err=%v", deleteTarget, err)
	}
	queuedID := "integration-queued-run"
	if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		_, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO scheduled_job_runs(id,scheduled_job_id,job_code,job_name,handler,trigger_source,status,request_id,trace_id,started_at,finished_at,duration_ms,error_message,payload,timeout_seconds,lock_ttl_seconds,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,NULL,NULL,0,'',?,?,?,?,?,?,?,1)`), queuedID, updated.ID, updated.Code, updated.Name, updated.Handler, "manual", "queued", "integration-request", "", string(updated.Payload), updated.TimeoutSeconds, updated.LockTTLSeconds, now, "scheduler-admin", now, "scheduler-admin")
		return err
	}); err != nil {
		t.Fatalf("insert queued scheduled job run: %v", err)
	}
	queuedRun, err := service.GetRun(actorCtx, queuedID)
	if err != nil || queuedRun.Status != "queued" || queuedRun.StartedAt != nil || queuedRun.FinishedAt != nil || queuedRun.Version != 1 {
		t.Fatalf("queued scheduled job run = %+v err=%v", queuedRun, err)
	}
	queuedPage, err := service.PageRuns(actorCtx, scheduler.RunPageInput{Request: pagination.Request{Page: 1, PageSize: 20}, ScheduledJobID: updated.ID, Statuses: []string{"queued"}})
	if err != nil || queuedPage.Total != 1 || len(queuedPage.Items) != 1 || queuedPage.Items[0].ID != queuedID {
		t.Fatalf("queued scheduled job page = %+v err=%v", queuedPage, err)
	}
	if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		_, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE scheduled_job_runs SET status='error',started_at=?,finished_at=?,error_message='integration cleanup',updated_at=?,updated_by=? WHERE id=? AND version=? AND deleted_at IS NULL`), now, now, now, "scheduler-admin", queuedID, queuedRun.Version)
		return err
	}); err != nil {
		t.Fatalf("complete queued scheduled job fixture: %v", err)
	}

	if err := runtime.Trigger(actorCtx, updated); err != nil {
		t.Fatalf("trigger scheduled job: %v", err)
	}
	runs, err := service.PageRuns(actorCtx, scheduler.RunPageInput{
		Request:        pagination.Request{Page: 1, PageSize: 20},
		ScheduledJobID: updated.ID,
		Statuses:       []string{"success"},
		TriggerSources: []string{"manual"},
	})
	if err != nil || runs.Total != 1 || len(runs.Items) != 1 {
		t.Fatalf("page scheduled job runs = %+v err=%v", runs, err)
	}
	run, err := service.GetRun(actorCtx, runs.Items[0].ID)
	if err != nil || run.ScheduledJobID != updated.ID || run.JobName != updated.Name || run.Version != 1 {
		t.Fatalf("get scheduled job run = %+v err=%v", run, err)
	}
	testScheduledJobCompletionOutboxTransaction(t, ctx, db, transactor, handlers, updated)
	testManualScheduledJobOutboxTransaction(t, ctx, db, transactor, updated.ID)

	if err := service.Delete(actorCtx, updated.ID, updated.Version); err != nil {
		t.Fatalf("delete scheduled job: %v", err)
	}
	if _, err := service.Get(actorCtx, updated.ID); !errors.Is(err, scheduler.ErrDefinitionNotFound) {
		t.Fatalf("get deleted scheduled job error = %v", err)
	}
	if _, err := service.UpdateTarget(actorCtx, updated.ID); !errors.Is(err, scheduler.ErrDefinitionNotFound) {
		t.Fatalf("get deleted scheduled job update target error = %v", err)
	}
}

func testScheduledJobCompletionOutboxTransaction(t *testing.T, ctx context.Context, db *sqlx.DB, transactor *appdb.Transactor, handlers *scheduler.HandlerRegistry, definition scheduler.Definition) {
	t.Helper()
	const subject = "platform.operation-log.v1"
	cfg := config.Config{
		App:          config.App{Name: "scheduler-integration"},
		Cron:         config.Cron{Timezone: "Asia/Shanghai"},
		EventBus:     config.EventBus{Enabled: true},
		OperationLog: config.OperationLog{Enabled: true, Subject: subject, MaxPayloadBytes: 4096},
	}
	bus := &eventbus.Bus{}
	outbox := eventbus.NewOutbox(cfg, bus, transactor, nil, nil)
	operations := operationlog.New(cfg, bus, db, transactor, nil, outbox, nil)
	manager, err := scheduler.NewManager(integrationLifecycle{}, cfg, db, transactor, integrationLifecycleLocker{}, observability.NewMetrics(cfg, nil, nil), slog.New(slog.NewTextHandler(io.Discard, nil)), handlers, operations)
	if err != nil {
		t.Fatalf("create logged scheduler runtime: %v", err)
	}
	actorCtx := platformprincipal.SystemContext(ctx, "scheduler-admin")
	countRuns := func() int {
		t.Helper()
		var count int
		if err := db.GetContext(ctx, &count, db.Rebind(`SELECT count(*) FROM scheduled_job_runs WHERE scheduled_job_id=? AND trigger_source='manual' AND status='success' AND deleted_at IS NULL`), definition.ID); err != nil {
			t.Fatal(err)
		}
		return count
	}
	countEvents := func() int {
		t.Helper()
		var count int
		if err := db.GetContext(ctx, &count, db.Rebind(`SELECT count(*) FROM event_outbox WHERE subject=? AND deleted_at IS NULL`), subject); err != nil {
			t.Fatal(err)
		}
		return count
	}
	runsBefore, eventsBefore := countRuns(), countEvents()
	if err := manager.Trigger(actorCtx, definition); err != nil {
		t.Fatalf("execute logged scheduled job: %v", err)
	}
	if got := countRuns(); got != runsBefore+1 {
		t.Fatalf("completed scheduled job runs = %d, want %d", got, runsBefore+1)
	}
	if got := countEvents(); got != eventsBefore+1 {
		t.Fatalf("completion outbox events = %d, want %d", got, eventsBefore+1)
	}
	var rawEnvelope []byte
	if err := db.GetContext(ctx, &rawEnvelope, db.Rebind(`SELECT envelope FROM event_outbox WHERE subject=? AND deleted_at IS NULL ORDER BY created_at DESC,id DESC LIMIT 1`), subject); err != nil {
		t.Fatal(err)
	}
	var envelope commonv1.EventEnvelope
	if err := proto.Unmarshal(rawEnvelope, &envelope); err != nil || envelope.AggregateId == "" || envelope.EventType != "platform.operation-log.recorded.v1" {
		t.Fatalf("completion event = %+v, err=%v", envelope, err)
	}
	var linkedRuns int
	if err := db.GetContext(ctx, &linkedRuns, db.Rebind(`SELECT count(*) FROM scheduled_job_runs WHERE id=? AND scheduled_job_id=? AND trigger_source='manual' AND status='success' AND deleted_at IS NULL`), envelope.AggregateId, definition.ID); err != nil || linkedRuns != 1 {
		t.Fatalf("completion event linked runs = %d, run_id=%q, err=%v", linkedRuns, envelope.AggregateId, err)
	}

	unavailable := eventbus.NewOutbox(config.Config{App: cfg.App}, bus, transactor, nil, nil)
	operations = operationlog.New(cfg, bus, db, transactor, nil, unavailable, nil)
	manager, err = scheduler.NewManager(integrationLifecycle{}, cfg, db, transactor, integrationLifecycleLocker{}, observability.NewMetrics(cfg, nil, nil), slog.New(slog.NewTextHandler(io.Discard, nil)), handlers, operations)
	if err != nil {
		t.Fatalf("create scheduler with unavailable log outbox: %v", err)
	}
	if err := manager.Trigger(actorCtx, definition); err == nil {
		t.Fatal("scheduled job completion succeeded without durable operation log")
	}
	if got := countRuns(); got != runsBefore+1 {
		t.Fatalf("failed log enqueue persisted a completed run: count=%d", got)
	}
	if got := countEvents(); got != eventsBefore+1 {
		t.Fatalf("failed log enqueue persisted an event: count=%d", got)
	}
}

func testManualScheduledJobOutboxTransaction(t *testing.T, ctx context.Context, db *sqlx.DB, transactor *appdb.Transactor, jobID string) {
	t.Helper()
	actorCtx := requestid.WithContext(platformprincipal.SystemContext(ctx, "scheduler-admin"), "integration-manual-request")
	cfg := config.Config{App: config.App{Name: "scheduler-integration"}, EventBus: config.EventBus{Enabled: true}}
	outbox := eventbus.NewOutbox(cfg, &eventbus.Bus{}, transactor, nil, nil)
	service := scheduler.NewDefinitionService(db, transactor, nil, nil, nil, nil, outbox)

	result, err := service.Trigger(actorCtx, jobID)
	if err != nil || result.Status != "queued" || result.RunID == "" {
		t.Fatalf("submit manual scheduled job = %+v err=%v", result, err)
	}
	var persisted struct {
		Status    string `db:"status"`
		RequestID string `db:"request_id"`
		Version   int64  `db:"version"`
	}
	if err := db.GetContext(ctx, &persisted, db.Rebind(`SELECT status,request_id,version FROM scheduled_job_runs WHERE id=? AND deleted_at IS NULL`), result.RunID); err != nil || persisted.Status != "queued" || persisted.RequestID != "integration-manual-request" {
		t.Fatalf("persisted manual run = %+v err=%v", persisted, err)
	}
	var events int
	if err := db.GetContext(ctx, &events, db.Rebind(`SELECT count(*) FROM event_outbox WHERE id=? AND subject=? AND deleted_at IS NULL`), result.RunID, "platform.scheduled-job.triggered.v1"); err != nil || events != 1 {
		t.Fatalf("manual run outbox events = %d err=%v", events, err)
	}

	unavailable := eventbus.NewOutbox(config.Config{App: cfg.App}, &eventbus.Bus{}, transactor, nil, nil)
	service = scheduler.NewDefinitionService(db, transactor, nil, nil, nil, nil, unavailable)
	if _, err := service.Trigger(actorCtx, jobID); err == nil {
		t.Fatal("manual dispatch without an available outbox succeeded")
	}
	var queuedRuns int
	if err := db.GetContext(ctx, &queuedRuns, db.Rebind(`SELECT count(*) FROM scheduled_job_runs WHERE scheduled_job_id=? AND status='queued' AND trigger_source='manual' AND deleted_at IS NULL`), jobID); err != nil || queuedRuns != 1 {
		t.Fatalf("manual runs after rejected dispatch = %d, want 1 committed run, err=%v", queuedRuns, err)
	}
	if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		updated, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE scheduled_job_runs SET status='error',started_at=?,finished_at=?,error_message='integration cleanup',updated_at=?,updated_by=? WHERE id=? AND version=? AND status='queued' AND deleted_at IS NULL`), now, now, now, "scheduler-admin", result.RunID, persisted.Version)
		if err != nil {
			return err
		}
		rows, err := updated.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("cleanup manual run affected %d rows", rows)
		}
		return nil
	}); err != nil {
		t.Fatalf("complete manual run fixture: %v", err)
	}
}

type integrationStorage struct {
	deleted   []string
	deleteErr error
}

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
	return s.deleteErr
}
func (*integrationStorage) Presign(_ context.Context, input objectstorage.PresignInput) (objectstorage.SignedURL, error) {
	return objectstorage.SignedURL{URL: "https://files.example/download", Method: "GET", ExpiresAt: time.Now().Add(input.TTL)}, nil
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
	storage.deleteErr = errors.New("object deletion failed for signed URL with secret-value")
	failedOrphanID := "failed-orphan-" + created.ID
	if err := appdb.NewTransactor(db).Within(ownerCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		_, err := tx.ExecContext(ownerCtx, tx.Rebind(`INSERT INTO file_upload_intents (id,tenant_id,object_key,status,attempts,last_error,next_attempt_at,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`), failedOrphanID, "file-tenant", "files/file-tenant/"+failedOrphanID+"/orphan.txt", "pending", 0, "", now.Add(-time.Minute), now, "file-owner", now, "file-owner", 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessPendingUploads(ownerCtx); err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("failed upload repair exposed storage error: %v", err)
	}
	var uploadError string
	if err := db.GetContext(ctx, &uploadError, db.Rebind(`SELECT last_error FROM file_upload_intents WHERE id=?`), failedOrphanID); err != nil || uploadError != "object_delete_failed" {
		t.Fatalf("failed upload repair reason=%q err=%v", uploadError, err)
	}
	failedDeletion, err := service.Upload(ownerCtx, files.UploadInput{Name: "failed-deletion.txt", Size: 5, Body: bytes.NewBufferString("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ownerCtx, failedDeletion.ID, failedDeletion.Version); err != nil {
		t.Fatal(err)
	}
	var objectDeleteError string
	if err := db.GetContext(ctx, &objectDeleteError, db.Rebind(`SELECT object_delete_error FROM files WHERE id=?`), failedDeletion.ID); err != nil || objectDeleteError != "object_delete_failed" {
		t.Fatalf("failed object deletion reason=%q err=%v", objectDeleteError, err)
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
	adminDeleteTarget, err := service.AdminDeleteTarget(createCtx, created.ID)
	if err != nil || adminDeleteTarget.ID != created.ID || adminDeleteTarget.Version != created.Version {
		t.Fatalf("platform tenant delete target=%+v err=%v", adminDeleteTarget, err)
	}
	adminUpdateTarget, err := service.AdminUpdateTarget(createCtx, created.ID)
	if err != nil || adminUpdateTarget.ID != created.ID || adminUpdateTarget.Version != created.Version || adminUpdateTarget.Name != created.Name {
		t.Fatalf("platform tenant update target=%+v err=%v", adminUpdateTarget, err)
	}
	adminPage, err := service.AdminPage(createCtx, tenant.PageInput{Request: pagination.Request{Page: 1, PageSize: 20}, IDs: []string{created.ID}, Statuses: []tenant.Status{tenant.StatusActive}})
	if err != nil || adminPage.Total != 1 || len(adminPage.Items) != 1 {
		t.Fatalf("admin tenant page=%+v err=%v", adminPage, err)
	}
	tenantCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "owner-1", Type: platformprincipal.TypeUser, TenantID: created.ID})
	tenantPage, err := service.Page(tenantCtx, tenant.PageInput{Request: pagination.Request{Page: 1, PageSize: 20}, IDs: []string{created.ID}})
	if err != nil || tenantPage.Total != 1 || len(tenantPage.Items) != 1 || tenantPage.Items[0].ID != created.ID {
		t.Fatalf("tenant-scoped page=%+v err=%v", tenantPage, err)
	}
	deleteTarget, err := service.DeleteTarget(tenantCtx)
	if err != nil || deleteTarget.ID != created.ID || deleteTarget.Version != created.Version || deleteTarget.Name != created.Name {
		t.Fatalf("current tenant delete target=%+v err=%v", deleteTarget, err)
	}
	updateTarget, err := service.UpdateTarget(tenantCtx)
	if err != nil || updateTarget.ID != created.ID || updateTarget.Version != created.Version || updateTarget.Name != created.Name {
		t.Fatalf("current tenant update target=%+v err=%v", updateTarget, err)
	}
	otherTenantCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "other", Type: platformprincipal.TypeUser, TenantID: "another-tenant"})
	otherTenantPage, err := service.Page(otherTenantCtx, tenant.PageInput{Request: pagination.Request{Page: 1, PageSize: 20}, IDs: []string{created.ID}})
	if err != nil || otherTenantPage.Total != 0 || len(otherTenantPage.Items) != 0 {
		t.Fatalf("cross-tenant page leaked data=%+v err=%v", otherTenantPage, err)
	}
	if _, err := service.DeleteTarget(otherTenantCtx); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("other tenant delete target error=%v", err)
	}
	if _, err := service.UpdateTarget(otherTenantCtx); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("other tenant update target error=%v", err)
	}
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
	updateTarget, err = service.UpdateTarget(tenantCtx)
	if err != nil || updateTarget.Version != updated.Version || updateTarget.Name != updated.Name {
		t.Fatalf("updated tenant update target=%+v err=%v", updateTarget, err)
	}
	adminDeleteTarget, err = service.AdminDeleteTarget(createCtx, created.ID)
	if err != nil || adminDeleteTarget.Version != updated.Version || adminDeleteTarget.Name != updated.Name {
		t.Fatalf("updated platform delete target=%+v err=%v", adminDeleteTarget, err)
	}
	adminUpdateTarget, err = service.AdminUpdateTarget(createCtx, created.ID)
	if err != nil || adminUpdateTarget.Version != updated.Version || adminUpdateTarget.Name != updated.Name {
		t.Fatalf("updated platform tenant target=%+v err=%v", adminUpdateTarget, err)
	}
	if _, err := service.Update(tenantCtx, tenant.UpdateInput{ID: created.ID, Name: "Stale", Status: tenant.StatusActive, Version: adminUpdated.Version}); !errors.Is(err, tenant.ErrStaleVersion) {
		t.Fatalf("stale update error = %v", err)
	}
	var membershipID string
	if err := db.GetContext(ctx, &membershipID, db.Rebind(`SELECT id FROM tenant_memberships WHERE tenant_id=? AND user_id=? AND deleted_at IS NULL`), created.ID, "owner-1"); err != nil {
		t.Fatal(err)
	}
	// This integration test exercises repository lifecycle behavior directly.
	// System callers are the only callers allowed to bypass endpoint data scopes.
	departmentCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "owner-1", Type: platformprincipal.TypeSystem, TenantID: created.ID, MembershipID: membershipID})
	members := tenant.NewMembershipService(tenant.NewRepository(db), appdb.NewTransactor(db), staticUserResolver{id: "member-2", username: "member.two", name: "Member Two"}, nil, nil, config.Config{}, discardOperationRecorder{}, discardSecurityRecorder{}, nil)
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
	departments := tenant.NewDepartmentService(db, appdb.NewTransactor(db), nil, discardOperationRecorder{}, nil, staticUserResolver{id: "member-2", username: "member.two", name: "Member Two"}, config.Config{}, nil)
	root, err := departments.Create(departmentCtx, tenant.DepartmentInput{Code: "engineering", Name: "研发中心"})
	if err != nil {
		t.Fatal(err)
	}
	if target, err := departments.UpdateTarget(departmentCtx, root.ID); err != nil || target.ID != root.ID || target.Version != root.Version || target.ParentID != nil {
		t.Fatalf("department update target=%+v err=%v", target, err)
	}
	if target, err := departments.DeleteTarget(departmentCtx, root.ID); err != nil || target.ID != root.ID || target.Version != root.Version {
		t.Fatalf("department delete target=%+v err=%v", target, err)
	}
	otherDepartmentCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "other", Type: platformprincipal.TypeSystem, TenantID: "another-tenant", MembershipID: "other-member"})
	if _, err := departments.UpdateTarget(otherDepartmentCtx, root.ID); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("cross-tenant department update target error=%v", err)
	}
	testDepartmentDataPermissionEnforcement(t, ctx, db, created.ID, membershipID, &root)
	child, err := departments.Create(departmentCtx, tenant.DepartmentInput{ParentID: &root.ID, Code: "backend", Name: "后端平台"})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := departments.Tree(departmentCtx, "backend")
	if err != nil || len(tree) != 1 || len(tree[0].Children) != 1 {
		t.Fatalf("department tree=%+v err=%v", tree, err)
	}
	if err := departments.SetMembers(departmentCtx, child.ID, child.Version, []tenant.DepartmentMemberAssignment{{MembershipID: membershipID, IsPrimary: true}}); err != nil {
		t.Fatal(err)
	}
	if err := departments.Delete(departmentCtx, child.ID, child.Version); !errors.Is(err, tenant.ErrConflict) {
		t.Fatalf("delete assigned department error=%v", err)
	}
	if err := departments.SetMembers(departmentCtx, child.ID, child.Version+1, nil); err != nil {
		t.Fatal(err)
	}
	if err := departments.Delete(departmentCtx, child.ID, child.Version); !errors.Is(err, tenant.ErrStaleVersion) {
		t.Fatalf("stale department delete error=%v", err)
	}
	if err := departments.SetMembers(departmentCtx, child.ID, child.Version+1, nil); !errors.Is(err, tenant.ErrStaleVersion) {
		t.Fatalf("stale department assignment error=%v", err)
	}
	if err := departments.Delete(departmentCtx, child.ID, child.Version+2); err != nil {
		t.Fatal(err)
	}
	if _, err := departments.DeleteTarget(departmentCtx, child.ID); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("deleted department target error=%v", err)
	}
	if err := departments.Delete(departmentCtx, root.ID, root.Version); err != nil {
		t.Fatal(err)
	}
	otherTenant, err := service.Create(createCtx, tenant.CreateInput{Code: "integration-policy-other", Name: "Other Policy Tenant", OwnerUsername: "owner.one"})
	if err != nil {
		t.Fatal(err)
	}
	policyIDs := map[string][3]string{}
	authenticated := true
	for _, fixture := range []struct {
		name, scope, tenantID string
	}{
		{name: "deleted", scope: "tenant", tenantID: created.ID},
		{name: "other", scope: "tenant", tenantID: otherTenant.ID},
		{name: "global", scope: "global"},
	} {
		ids := [3]string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
		policyIDs[fixture.name] = ids
		code := "integration-tenant-delete-" + fixture.name
		document, err := datapermission.MarshalPolicy(datapermission.Policy{
			APIVersion: datapermission.PolicyAPIVersion,
			Kind:       datapermission.PolicyKind,
			Metadata:   datapermission.PolicyMetadata{Code: code, Name: fixture.name},
			Scope:      datapermission.PolicyBoundary{Type: datapermission.PolicyScopeType(fixture.scope), TenantID: fixture.tenantID},
			Spec: datapermission.PolicySpec{
				Subject:   resourceaction.SubjectMatcher{Authenticated: &authenticated},
				Resource:  "tenant.member",
				Actions:   []string{"read"},
				Condition: "resource.owner_id == subject.id",
				Effect:    datapermission.EffectAllow,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		var tenantArg any
		if fixture.scope == "tenant" {
			tenantArg = fixture.tenantID
		}
		if err := appdb.NewTransactor(db).Within(createCtx, nil, func(tx *sqlx.Tx) error {
			now := time.Now()
			if _, err := tx.ExecContext(createCtx, tx.Rebind(`INSERT INTO data_permission_policies(id,code,name,scope,tenant_id,published_version_number,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`), ids[0], code, fixture.name, fixture.scope, tenantArg, 1, "active", now, "owner-1", now, "owner-1"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(createCtx, tx.Rebind(`INSERT INTO data_permission_policy_versions(id,policy_id,version_number,document,status,published_at,published_by,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`), ids[1], ids[0], 1, string(document), "published", now, "owner-1", now, "owner-1", now, "owner-1"); err != nil {
				return err
			}
			_, err := tx.ExecContext(createCtx, tx.Rebind(`INSERT INTO data_permission_policy_actions(id,policy_version_id,resource,action,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,1)`), ids[2], ids[1], "tenant.member", "read", now, "owner-1", now, "owner-1")
			return err
		}); err != nil {
			t.Fatalf("seed %s data permission policy: %v", fixture.name, err)
		}
	}
	if err := service.AdminDelete(createCtx, created.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	for name, ids := range policyIDs {
		wantActive := 1
		if name == "deleted" {
			wantActive = 0
		}
		for index, table := range []string{"data_permission_policies", "data_permission_policy_versions", "data_permission_policy_actions"} {
			var active int
			if err := db.GetContext(ctx, &active, db.Rebind(`SELECT count(*) FROM `+table+` WHERE id=? AND deleted_at IS NULL`), ids[index]); err != nil || active != wantActive {
				t.Fatalf("%s policy %s active=%d, want %d, err=%v", name, table, active, wantActive, err)
			}
		}
	}
	if err := appdb.NewTransactor(db).Within(createCtx, nil, func(tx *sqlx.Tx) error {
		globalIDs := policyIDs["global"]
		for _, cleanup := range []struct{ table, id string }{
			{table: "data_permission_policy_actions", id: globalIDs[2]},
			{table: "data_permission_policy_versions", id: globalIDs[1]},
			{table: "data_permission_policies", id: globalIDs[0]},
		} {
			if _, err := tx.ExecContext(createCtx, tx.Rebind(`UPDATE `+cleanup.table+` SET deleted_at=? WHERE id=? AND deleted_at IS NULL`), time.Now(), cleanup.id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("cleanup global policy fixture: %v", err)
	}
	if err := service.AdminDelete(createCtx, otherTenant.ID, otherTenant.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(tenantCtx, created.ID); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("get deleted tenant error = %v", err)
	}
	if _, err := service.DeleteTarget(tenantCtx); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("deleted tenant confirmation error=%v", err)
	}
	if _, err := service.AdminDeleteTarget(createCtx, created.ID); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("deleted platform tenant confirmation error=%v", err)
	}
	if _, err := service.AdminUpdateTarget(createCtx, created.ID); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("deleted platform tenant update target error=%v", err)
	}
}

func testDepartmentDataPermissionEnforcement(t *testing.T, ctx context.Context, db *sqlx.DB, tenantID, membershipID string, visible *tenant.Department) {
	t.Helper()
	const hiddenID = "22ae8148-6223-435c-810d-aac2a7c34a30"
	now := time.Now()
	actorCtx := platformprincipal.SystemContext(ctx, "other-actor")
	if err := appdb.NewTransactor(db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(actorCtx, tx.Rebind(`INSERT INTO tenant_departments(id,tenant_id,parent_id,code,name,sort_order,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,1)`), hiddenID, tenantID, nil, "hidden-department", "Hidden Department", 0, now, "other-actor", now, "other-actor")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := datapermission.NewSchemaRegistry(tenant.NewDepartmentDataPermissionSchema())
	if err != nil {
		t.Fatal(err)
	}
	authenticated := true
	engine, err := datapermission.NewEngine(schemas, resources, []datapermission.Policy{{
		APIVersion: datapermission.PolicyAPIVersion, Kind: datapermission.PolicyKind,
		Metadata: datapermission.PolicyMetadata{Code: "integration-department-owner", Name: "Integration department owner scope"},
		Scope:    datapermission.PolicyBoundary{Type: datapermission.PolicyScopeTenant, TenantID: tenantID},
		Spec:     datapermission.PolicySpec{Subject: resourceaction.SubjectMatcher{Authenticated: &authenticated}, Resource: "tenant.department", Actions: []string{"read", "list", "update", "delete", "assign-member"}, Condition: "resource.created_by == subject.id", Effect: datapermission.EffectAllow},
	}})
	if err != nil {
		t.Fatal(err)
	}
	service := tenant.NewDepartmentService(db, appdb.NewTransactor(db), nil, discardOperationRecorder{}, nil, staticUserResolver{id: "owner-1", username: "owner", name: "Owner"}, config.Config{}, datapermission.NewService(db, engine))
	principal := platformprincipal.Principal{ID: "owner-1", Type: platformprincipal.TypeUser, TenantID: tenantID, MembershipID: membershipID}
	endpointContext := func(action string) context.Context {
		requestCtx := platformprincipal.WithContext(ctx, principal)
		return accesscontrol.WithEndpoint(requestCtx, accesscontrol.Endpoint{Resource: "tenant.department", Action: action, DataPermission: accesscontrol.DataPermissionRequired})
	}
	if _, err := service.Get(endpointContext("read"), hiddenID); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("hidden department get error=%v", err)
	}
	if _, err := service.UpdateTarget(endpointContext("update"), hiddenID); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("hidden department update target error=%v", err)
	}
	if _, err := service.DeleteTarget(endpointContext("delete"), hiddenID); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("hidden department delete target error=%v", err)
	}
	if target, err := service.UpdateTarget(endpointContext("update"), visible.ID); err != nil || target.ID != visible.ID || target.Version != visible.Version {
		t.Fatalf("visible department update target=%+v err=%v", target, err)
	}
	if _, err := service.Get(endpointContext("assign-member"), hiddenID); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("hidden department assignment target error=%v", err)
	}
	if target, err := service.Get(endpointContext("assign-member"), visible.ID); err != nil || target.ID != visible.ID || target.Version != visible.Version {
		t.Fatalf("visible department assignment target=%+v err=%v", target, err)
	}
	tree, err := service.Tree(endpointContext("list"), "")
	if err != nil || len(tree) != 1 || tree[0].ID != visible.ID {
		t.Fatalf("data-scoped department tree=%+v err=%v", tree, err)
	}
	if _, err := service.Update(endpointContext("update"), tenant.DepartmentUpdate{ID: hiddenID, Name: "Denied", Version: 1}); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("hidden department update error=%v", err)
	}
	updated, err := service.Update(endpointContext("update"), tenant.DepartmentUpdate{ID: visible.ID, Name: visible.Name, SortOrder: visible.SortOrder, Version: visible.Version})
	if err != nil {
		t.Fatalf("data-scoped department update: %v", err)
	}
	*visible = updated
	if err := service.Delete(endpointContext("delete"), hiddenID, 1); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("hidden department delete error=%v", err)
	}
}

func testMemberDataPermissionEnforcement(t *testing.T, ctx context.Context, db *sqlx.DB, tenantID, ownerMembershipID string, member tenant.Member) {
	t.Helper()
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
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
			Subject:   resourceaction.SubjectMatcher{Authenticated: &authenticated},
			Resource:  "tenant.member",
			Actions:   []string{"read", "list", "remove"},
			Condition: "resource.owner_id == subject.id",
			Effect:    datapermission.EffectAllow,
		},
	}, {
		APIVersion: datapermission.PolicyAPIVersion,
		Kind:       datapermission.PolicyKind,
		Metadata:   datapermission.PolicyMetadata{Code: "integration-member-self-update", Name: "Integration active member update"},
		Scope:      datapermission.PolicyBoundary{Type: datapermission.PolicyScopeTenant, TenantID: tenantID},
		Spec: datapermission.PolicySpec{
			Subject: resourceaction.SubjectMatcher{Authenticated: &authenticated}, Resource: "tenant.member", Actions: []string{"update"},
			Condition: "resource.owner_id == subject.id", ProposedCondition: `proposed.status == "active"`, Effect: datapermission.EffectAllow,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	scopes := datapermission.NewService(db, engine)
	service := tenant.NewMembershipService(tenant.NewRepository(db), appdb.NewTransactor(db), staticUserResolver{id: member.UserID, username: member.Username, name: member.DisplayName}, staticMemberContactSearcher{ids: []string{member.UserID}}, nil, config.Config{}, discardOperationRecorder{}, discardSecurityRecorder{}, scopes)
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
	contactPage, err := service.Page(endpointContext("list", accesscontrol.DataPermissionRequired), tenant.MemberPageInput{Request: pagination.Request{Page: 1, PageSize: 20}, Emails: []string{"member@example.com"}})
	if err != nil || contactPage.Total != 1 || len(contactPage.Items) != 1 || contactPage.Items[0].ID != member.ID {
		t.Fatalf("contact-filtered member page=%+v err=%v", contactPage, err)
	}
	rowProviders, err := authorization.NewRowCapabilityRegistry([]authorization.RowCapabilityProvider{tenant.NewMemberCapabilityProvider(db)}, resources, schemas)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := authorization.NewPolicyRevisions(nil, allowCapabilityAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := authorization.NewCapabilityService(allowCapabilityAuthorizer{}, scopes, resources, rowProviders, revisions)
	if err != nil {
		t.Fatal(err)
	}
	capabilityEndpoints, err := accesscontrol.NewEndpointRegistry(resources, []accesscontrol.Endpoint{{
		Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/tenant-members/status/update", Authentication: accesscontrol.AuthenticationJWT,
		Resource: "tenant.member", Action: "update", DataPermission: accesscontrol.DataPermissionRequired,
	}})
	if err != nil {
		t.Fatal(err)
	}
	capabilities.SetEndpointRegistry(capabilityEndpoints)
	capabilityResult, err := capabilities.EvaluateRows(platformprincipal.WithContext(ctx, principal), "tenant.member", []string{"update"}, []string{member.ID, ownerMembershipID, "missing-member"})
	if err != nil || len(capabilityResult.Items) != 3 || !capabilityResult.Items[0].Actions["update"] || capabilityResult.Items[1].Actions["update"] || capabilityResult.Items[2].Actions["update"] {
		t.Fatalf("row capability result=%+v err=%v", capabilityResult, err)
	}
	if _, err := service.UpdateStatus(endpointContext("update", accesscontrol.DataPermissionRequired), ownerMembershipID, tenant.StatusDisabled, 1); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("hidden member update error=%v", err)
	}
	if _, err := service.UpdateStatus(endpointContext("update", accesscontrol.DataPermissionRequired), member.ID, tenant.StatusDisabled, member.Version); !errors.Is(err, tenant.ErrForbidden) {
		t.Fatalf("proposed member status error=%v", err)
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

type staticMemberContactSearcher struct{ ids []string }

func (s staticMemberContactSearcher) SearchContactUserIDs(context.Context, []string, []string) ([]string, error) {
	return s.ids, nil
}

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
