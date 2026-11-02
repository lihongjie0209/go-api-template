//go:build integration

package integration

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
)

// assertMandatoryAuditColumns checks the migrated schema itself, including
// association tables. The migration history table is owned by golang-migrate.
func assertMandatoryAuditColumns(t *testing.T, ctx context.Context, db *sqlx.DB, databaseType, migrationTable string) {
	t.Helper()
	schema := "current_schema()"
	if databaseType == "mysql" {
		schema = "DATABASE()"
	}
	query := `SELECT t.table_name, c.column_name, c.is_nullable, c.data_type
		FROM information_schema.tables t
		JOIN information_schema.columns c
			ON c.table_schema=t.table_schema AND c.table_name=t.table_name
		WHERE t.table_schema=` + schema + ` AND t.table_type='BASE TABLE'`
	var rows []struct {
		Table    string `db:"table_name"`
		Column   string `db:"column_name"`
		Nullable string `db:"is_nullable"`
		DataType string `db:"data_type"`
	}
	if err := db.SelectContext(ctx, &rows, query); err != nil {
		t.Fatalf("load migrated table columns: %v", err)
	}
	type columnContract struct {
		nullable string
		dataType string
	}
	tables := make(map[string]map[string]columnContract)
	for _, row := range rows {
		if strings.EqualFold(row.Table, migrationTable) {
			continue
		}
		if tables[row.Table] == nil {
			tables[row.Table] = make(map[string]columnContract)
		}
		tables[row.Table][strings.ToLower(row.Column)] = columnContract{nullable: row.Nullable, dataType: strings.ToLower(row.DataType)}
	}
	if len(tables) == 0 {
		t.Fatal("migrated schema has no business tables")
	}
	required := []string{"created_at", "created_by", "updated_at", "updated_by", "version", "deleted_at", "deleted_by"}
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		for _, column := range required {
			contract, ok := tables[name][column]
			if !ok {
				t.Errorf("migrated table %s is missing mandatory audit column %s", name, column)
				continue
			}
			if column != "deleted_at" && column != "deleted_by" && contract.nullable != "NO" {
				t.Errorf("migrated table %s audit column %s must be NOT NULL", name, column)
			}
			if (column == "deleted_at" || column == "deleted_by") && contract.nullable != "YES" {
				t.Errorf("migrated table %s audit column %s must allow NULL", name, column)
			}
			expectedType := auditColumnType(databaseType, column)
			if contract.dataType != expectedType {
				t.Errorf("migrated table %s audit column %s has type %s, want %s", name, column, contract.dataType, expectedType)
			}
		}
	}
	assertAuditTriggers(t, ctx, db, databaseType, names)
}

func auditColumnType(databaseType, column string) string {
	switch column {
	case "created_at", "updated_at", "deleted_at":
		if databaseType == "postgres" {
			return "timestamp with time zone"
		}
		return "timestamp"
	case "version":
		return "bigint"
	default:
		return "text"
	}
}

func assertAuditTriggers(t *testing.T, ctx context.Context, db *sqlx.DB, databaseType string, tables []string) {
	t.Helper()
	if databaseType == "postgres" {
		var audited []string
		err := db.SelectContext(ctx, &audited, `SELECT c.relname
			FROM pg_class c
			JOIN pg_namespace n ON n.oid=c.relnamespace
			JOIN pg_trigger tr ON tr.tgrelid=c.oid
			WHERE n.nspname=current_schema() AND tr.tgname='app_audit_row_trigger'
				AND tr.tgenabled <> 'D' AND tr.tgfoid='app_audit_row()'::regprocedure`)
		if err != nil {
			t.Fatalf("load PostgreSQL audit triggers: %v", err)
		}
		covered := make(map[string]struct{}, len(audited))
		for _, name := range audited {
			covered[name] = struct{}{}
		}
		for _, name := range tables {
			if _, ok := covered[name]; !ok {
				t.Errorf("migrated table %s is missing its enabled audit trigger", name)
			}
		}
		return
	}
	var triggers []struct {
		Table string `db:"event_object_table"`
		Name  string `db:"trigger_name"`
	}
	if err := db.SelectContext(ctx, &triggers, `SELECT event_object_table, trigger_name
		FROM information_schema.triggers
		WHERE trigger_schema=DATABASE() AND action_timing='BEFORE'`); err != nil {
		t.Fatalf("load MySQL audit triggers: %v", err)
	}
	covered := make(map[string]map[string]struct{}, len(tables))
	for _, trigger := range triggers {
		if covered[trigger.Table] == nil {
			covered[trigger.Table] = make(map[string]struct{})
		}
		covered[trigger.Table][trigger.Name] = struct{}{}
	}
	for _, name := range tables {
		for _, suffix := range []string{"_audit_bi", "_audit_bu"} {
			if _, ok := covered[name][name+suffix]; !ok {
				t.Errorf("migrated table %s is missing audit trigger %s", name, name+suffix)
			}
		}
	}
}
