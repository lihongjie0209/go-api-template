package operationlog

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

func TestGetScopesTenantPrincipalInSQL(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := &Service{db: sqlx.NewDb(database, "sqlmock")}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	query := regexp.QuoteMeta(`SELECT ` + recordColumns + ` FROM operation_logs WHERE id=? AND deleted_at IS NULL AND tenant_id=?`)
	mock.ExpectQuery(query).WithArgs("log-1", "tenant-1").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	_, err = service.Get(ctx, "log-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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
	where := `deleted_at IS NULL AND tenant_id=?`
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*) FROM operation_logs WHERE ` + where)).
		WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT `+recordColumns+` FROM operation_logs WHERE `+where+` ORDER BY occurred_at DESC,id DESC LIMIT ? OFFSET ?`)).
		WithArgs("tenant-1", 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	page, err := service.Page(ctx, PageInput{TenantIDs: []string{"tenant-2"}})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("Page() = %+v, %v", page, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
