package tenant

import (
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRepositoryPageUsesSameScopedFiltersForCountAndRows(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	repository := NewRepository(sqlx.NewDb(raw, "pgx"))
	where := `deleted_at IS NULL AND id = $1 AND code IN ($2, $3) AND owner_user_id IN ($4, $5)`
	args := []driver.Value{"tenant-a", "alpha", "beta", "owner-a", "owner-b"}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM tenants WHERE " + where)).WithArgs(args...).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("FROM tenants WHERE " + where + " ORDER BY created_at DESC, id LIMIT $6 OFFSET $7")).WithArgs(append(args, 20, 0)...).WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "description", "status", "owner_user_id", "owner_name", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	_, total, err := repository.Page(t.Context(), "tenant-a", PageInput{Codes: []string{"alpha", "beta"}, OwnerUserIDs: []string{"owner-a", "owner-b"}}, 20, 0)
	if err != nil || total != 0 {
		t.Fatalf("Page() total=%d, error=%v", total, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryPageRejectsMissingTenantScopeBeforeQuery(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	repository := NewRepository(sqlx.NewDb(raw, "pgx"))
	for _, test := range []struct{ name, tenantID string }{
		{name: "empty"},
		{name: "whitespace", tenantID: "  "},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := repository.Page(t.Context(), test.tenantID, PageInput{}, 20, 0)
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("Page(%q) error = %v, want ErrForbidden", test.tenantID, err)
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryAdminPageUsesExplicitUnscopedQuery(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	repository := NewRepository(sqlx.NewDb(raw, "pgx"))
	where := "deleted_at IS NULL AND code IN ($1, $2)"
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM tenants WHERE "+where)).
		WithArgs("alpha", "beta").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("FROM tenants WHERE "+where+" ORDER BY created_at DESC, id LIMIT $3 OFFSET $4")).
		WithArgs("alpha", "beta", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "description", "status", "owner_user_id", "owner_name", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	_, total, err := repository.AdminPage(t.Context(), PageInput{Codes: []string{"alpha", "beta"}}, 20, 0)
	if err != nil || total != 0 {
		t.Fatalf("AdminPage() total=%d, error=%v", total, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIsUniqueViolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "postgres and kingbase", err: &pgconn.PgError{Code: "23505"}, want: true},
		{name: "mysql", err: &mysqlDriver.MySQLError{Number: 1062}, want: true},
		{name: "wrapped", err: errors.Join(errors.New("insert"), &pgconn.PgError{Code: "23505"}), want: true},
		{name: "other", err: errors.New("network unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isUniqueViolation(tt.err); got != tt.want {
				t.Fatalf("isUniqueViolation() = %v, want %v", got, tt.want)
			}
		})
	}
}
