package migration

import (
	"net/url"
	"testing"
)

func TestWithMigrationTable(t *testing.T) {
	t.Parallel()
	result, err := withMigrationOptions("postgres://user:pass@db/app?sslmode=disable", "orders_db", "orders_schema_migrations", "orders")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(result)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("x-migrations-table"); got != "orders_schema_migrations" {
		t.Fatalf("x-migrations-table = %q", got)
	}
	if got := parsed.Query().Get("sslmode"); got != "disable" {
		t.Fatalf("sslmode = %q", got)
	}
	if got := parsed.Query().Get("search_path"); got != "orders" {
		t.Fatalf("search_path = %q", got)
	}
	if parsed.Path != "/orders_db" {
		t.Fatalf("database path = %q", parsed.Path)
	}
}

func TestWithMigrationTableMySQLDSN(t *testing.T) {
	t.Parallel()
	result, err := withMigrationOptions("mysql://app:app@tcp(mysql:3306)/app", "orders_db", "orders_schema_migrations", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	const expected = "mysql://app:app@tcp(mysql:3306)/orders_db?x-migrations-table=orders_schema_migrations"
	if result != expected {
		t.Fatalf("result = %q, want %q", result, expected)
	}
}
