package operationlog

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
	return map[string]string{"actor-1": "Current Actor", "audit-1": "Current Auditor"}, nil
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
	query := regexp.QuoteMeta(`SELECT ` + recordColumns + ` FROM operation_logs l WHERE l.id=? AND l.deleted_at IS NULL AND l.tenant_id=?`)
	mock.ExpectQuery(query).WithArgs("log-1", "tenant-1").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	_, err = service.Get(ctx, "log-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() = %v, want ErrNotFound", err)
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

func TestPresentPreservesHistoricalActorAndResourceSnapshots(t *testing.T) {
	t.Parallel()
	service := &Service{actors: changingActorResolver{}}
	records := []Record{{ActorID: "actor-1", ActorName: "Historical Actor", ResourceID: "resource-1", ResourceName: "Historical Resource", CreatedBy: "audit-1", UpdatedBy: "audit-1"}}
	if err := service.present(t.Context(), records); err != nil {
		t.Fatal(err)
	}
	if records[0].ActorName != "Historical Actor" || records[0].ResourceName != "Historical Resource" || records[0].CreatedByName != "Current Auditor" {
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
	if _, err := service.Page(ctx, PageInput{IDs: tooMany}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("Page() = %v, want ErrInvalidEntry", err)
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
	where := `l.deleted_at IS NULL AND l.tenant_id=?`
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*) FROM operation_logs l WHERE ` + where)).
		WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT `+recordColumns+` FROM operation_logs l WHERE `+where+` ORDER BY l.occurred_at DESC,l.id DESC LIMIT ? OFFSET ?`)).
		WithArgs("tenant-1", 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	page, err := service.Page(ctx, PageInput{TenantIDs: []string{"tenant-2"}})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("Page() = %+v, %v", page, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMenuUsageScopesPrincipalTenantAndHalfOpenWindow(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := &Service{db: sqlx.NewDb(database, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1",
	})
	to := time.Date(2026, time.September, 18, 1, 0, 0, 0, time.UTC)
	from := to.Add(-MenuUsageWindow)
	query := regexp.QuoteMeta(`SELECT l.application_id,l.resource_id AS menu_id,COUNT(*) AS click_count,MAX(l.occurred_at) AS last_clicked_at
FROM operation_logs l
WHERE l.tenant_id=? AND l.actor_id=? AND l.source='frontend' AND l.resource_type='menu_view' AND l.succeeded=?
  AND l.application_id<>'' AND l.resource_id<>'' AND l.occurred_at>=? AND l.occurred_at<? AND l.deleted_at IS NULL
GROUP BY l.application_id,l.resource_id
ORDER BY COUNT(*) DESC,MAX(l.occurred_at) DESC,l.application_id,l.resource_id
LIMIT ?`)
	mock.ExpectQuery(query).WithArgs("tenant-1", "user-1", true, from, to, MenuUsageLimit).WillReturnRows(
		sqlmock.NewRows([]string{"application_id", "menu_id", "click_count", "last_clicked_at"}).
			AddRow("app-1", "menu-1", 3, to.Add(-time.Hour)),
	)

	items, err := service.MenuUsage(ctx, from, to)
	if err != nil || len(items) != 1 || items[0].ClickCount != 3 {
		t.Fatalf("MenuUsage() = %+v, %v", items, err)
	}
	if got := items[0].LastClickedAt.Format(time.RFC3339); got != "2026-09-18T08:00:00+08:00" {
		t.Fatalf("last_clicked_at = %s", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMenuUsageRejectsInvalidPrincipalAndWindowBeforeQuery(t *testing.T) {
	t.Parallel()
	to := time.Now()
	tests := []struct {
		name      string
		principal platformprincipal.Principal
		from      time.Time
		to        time.Time
	}{
		{name: "service account", principal: platformprincipal.Principal{ID: "svc", Type: platformprincipal.TypeServiceAccount, TenantID: "tenant-1", MembershipID: "membership-1"}, from: to.Add(-time.Hour), to: to},
		{name: "missing tenant", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, MembershipID: "membership-1"}, from: to.Add(-time.Hour), to: to},
		{name: "missing membership", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"}, from: to.Add(-time.Hour), to: to},
		{name: "reversed window", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1"}, from: to, to: to.Add(-time.Hour)},
		{name: "unbounded window", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1"}, from: to.Add(-MenuUsageWindow - time.Second), to: to},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := platformprincipal.WithContext(t.Context(), test.principal)
			service := &Service{}
			if _, err := service.MenuUsage(ctx, test.from, test.to); !errors.Is(err, ErrInvalidEntry) {
				t.Fatalf("MenuUsage() = %v, want ErrInvalidEntry", err)
			}
		})
	}
}
