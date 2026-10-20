package securitylog

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type changingActorResolver struct{}

func (changingActorResolver) ResolveUserIDs(context.Context, []string) (map[string]string, error) {
	return map[string]string{"actor-1": "Current Actor", "subject-1": "Current Subject", "audit-1": "Current Auditor"}, nil
}

func TestGetScopesTenantPrincipalInSQL(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := &Service{db: sqlx.NewDb(database, "sqlmock")}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	query := regexp.QuoteMeta(`SELECT ` + recordColumns + ` FROM security_logs l WHERE l.id=? AND l.deleted_at IS NULL AND l.tenant_id=?`)
	mock.ExpectQuery(query).WithArgs("log-1", "tenant-1").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	_, err = service.Get(ctx, "log-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetProjectsCurrentTenantName(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := &Service{db: sqlx.NewDb(database, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	query := regexp.QuoteMeta(`SELECT ` + recordColumns + ` FROM security_logs l WHERE l.id=? AND l.deleted_at IS NULL AND l.tenant_id=?`)
	mock.ExpectQuery(query).WithArgs("log-1", "tenant-1").WillReturnRows(
		sqlmock.NewRows([]string{"id", "tenant_id", "tenant_name"}).AddRow("log-1", "tenant-1", "租户甲"),
	)
	record, err := service.Get(ctx, "log-1")
	if err != nil {
		t.Fatal(err)
	}
	if record.TenantName != "租户甲" || record.TenantID != "tenant-1" {
		t.Fatalf("unexpected log ownership projection: %+v", record)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQueriesRejectMissingTenantBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeUser,
	})
	service := &Service{}
	if _, err := service.Get(ctx, "log-1"); !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("Get() = %v, want ErrTenantRequired", err)
	}
	if _, err := service.Page(ctx, PageInput{TenantIDs: []string{"tenant-2"}}); !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("Page() = %v, want ErrTenantRequired", err)
	}
}

func TestSystemQueriesRequirePlatformContextAndBoundedWindow(t *testing.T) {
	t.Parallel()
	service := &Service{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	if _, err := service.SystemGet(ctx, "log-1"); !errors.Is(err, ErrPlatformContextRequired) {
		t.Fatalf("SystemGet() = %v, want ErrPlatformContextRequired", err)
	}
	if _, err := service.SystemPage(ctx, PageInput{}); !errors.Is(err, ErrPlatformContextRequired) {
		t.Fatalf("SystemPage() = %v, want ErrPlatformContextRequired", err)
	}
	ctx = platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	if _, err := service.SystemPage(ctx, PageInput{}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("SystemPage() = %v, want ErrInvalidEntry", err)
	}
	from := time.Now().Add(-32 * 24 * time.Hour)
	to := time.Now()
	if _, err := service.SystemPage(ctx, PageInput{OccurredAtFrom: &from, OccurredAtTo: &to}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("SystemPage(unbounded) = %v, want ErrInvalidEntry", err)
	}
}

func TestSystemGetScopesSQLToSystemTenant(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := &Service{db: sqlx.NewDb(database, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	query := regexp.QuoteMeta(`SELECT ` + recordColumns + ` FROM security_logs l WHERE l.id=? AND l.deleted_at IS NULL AND l.tenant_id=?`)
	mock.ExpectQuery(query).WithArgs("log-1", "").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if _, err := service.SystemGet(ctx, "log-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SystemGet() = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSystemPageScopesCountAndItemsToSystemTenant(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := &Service{db: sqlx.NewDb(database, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	from := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	where := `l.deleted_at IS NULL AND l.tenant_id=? AND trace_id IN (?) AND occurred_at>=? AND occurred_at<?`
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*) FROM security_logs l WHERE `+where)).
		WithArgs("", "trace-1", from, to).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT `+recordColumns+` FROM security_logs l WHERE `+where+` ORDER BY l.occurred_at DESC,l.id DESC LIMIT ? OFFSET ?`)).
		WithArgs("", "trace-1", from, to, 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	page, err := service.SystemPage(ctx, PageInput{TraceIDs: []string{"trace-1"}, OccurredAtFrom: &from, OccurredAtTo: &to})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("SystemPage() = %+v, %v", page, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPresentUsesAsiaShanghaiWithoutChangingInstant(t *testing.T) {
	t.Parallel()
	instant := time.Date(2026, time.September, 16, 1, 2, 3, 0, time.UTC)
	record := Record{OccurredAt: instant, CreatedAt: instant, UpdatedAt: instant}
	record = presentRecord(record)
	for name, value := range map[string]time.Time{"occurred_at": record.OccurredAt, "created_at": record.CreatedAt, "updated_at": record.UpdatedAt} {
		if !value.Equal(instant) || value.Format(time.RFC3339) != "2026-09-16T09:02:03+08:00" {
			t.Fatalf("%s = %s", name, value.Format(time.RFC3339))
		}
	}
}

func TestPresentPreservesHistoricalActorAndSubjectSnapshots(t *testing.T) {
	t.Parallel()
	service := &Service{actors: changingActorResolver{}}
	records := []Record{{ActorID: "actor-1", ActorName: "Historical Actor", SubjectID: "subject-1", SubjectName: "Historical Subject", CreatedBy: "audit-1", UpdatedBy: "audit-1"}}
	if err := service.present(t.Context(), records); err != nil {
		t.Fatal(err)
	}
	if records[0].ActorName != "Historical Actor" || records[0].SubjectName != "Historical Subject" || records[0].CreatedByName != "Current Auditor" {
		t.Fatalf("record=%+v", records[0])
	}
}

func TestPageRejectsUnboundedFiltersAndInvalidRange(t *testing.T) {
	t.Parallel()
	service := &Service{}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	tooMany := make([]string, 201)
	for index := range tooMany {
		tooMany[index] = "id"
	}
	now := time.Now()
	for name, input := range map[string]PageInput{
		"too many ids":    {IDs: tooMany},
		"too many traces": {TraceIDs: tooMany},
		"blank trace":     {TraceIDs: []string{" "}},
		"long trace":      {TraceIDs: []string{string(make([]byte, 65))}},
		"invalid range":   {OccurredAtFrom: &now, OccurredAtTo: &now},
		"invalid event":   {EventTypes: []EventType{"unknown"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Page(ctx, input); !errors.Is(err, ErrInvalidEntry) {
				t.Fatalf("Page() = %v, want ErrInvalidEntry", err)
			}
		})
	}
}

func TestPageScopesTenantBeforeApplyingClientFilters(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := &Service{db: sqlx.NewDb(database, "sqlmock")}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	where := `l.deleted_at IS NULL AND l.tenant_id=? AND l.tenant_id IN (?) AND trace_id IN (?)`
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*) FROM security_logs l WHERE `+where)).
		WithArgs("tenant-1", "tenant-2", "trace-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT `+recordColumns+` FROM security_logs l WHERE `+where+` ORDER BY l.occurred_at DESC,l.id DESC LIMIT ? OFFSET ?`)).
		WithArgs("tenant-1", "tenant-2", "trace-1", 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	page, err := service.Page(ctx, PageInput{TenantIDs: []string{"tenant-2"}, TraceIDs: []string{"trace-1"}})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("Page() = %+v, %v", page, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
