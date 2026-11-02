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
	query := `SELECT t.table_name AS table_key, c.column_name AS column_key,
		c.is_nullable AS nullable_flag, c.data_type AS type_key
		FROM information_schema.tables t
		JOIN information_schema.columns c
			ON c.table_schema=t.table_schema AND c.table_name=t.table_name
		WHERE t.table_schema=` + schema + ` AND t.table_type='BASE TABLE'`
	var rows []struct {
		Table    string `db:"table_key"`
		Column   string `db:"column_key"`
		Nullable string `db:"nullable_flag"`
		DataType string `db:"type_key"`
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
				AND tr.tgenabled IN ('O', 'A') AND tr.tgfoid='app_audit_row()'::regprocedure
				AND (tr.tgtype & 31) = 31`)
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
		Table     string `db:"table_key"`
		Name      string `db:"trigger_key"`
		Event     string `db:"event_key"`
		Statement string `db:"statement_text"`
	}
	if err := db.SelectContext(ctx, &triggers, `SELECT event_object_table AS table_key, trigger_name AS trigger_key,
		event_manipulation AS event_key, action_statement AS statement_text
		FROM information_schema.triggers
		WHERE trigger_schema=DATABASE() AND action_timing='BEFORE'`); err != nil {
		t.Fatalf("load MySQL audit triggers: %v", err)
	}
	type triggerContract struct {
		event     string
		statement string
	}
	covered := make(map[string]map[string]triggerContract, len(tables))
	for _, trigger := range triggers {
		if covered[trigger.Table] == nil {
			covered[trigger.Table] = make(map[string]triggerContract)
		}
		covered[trigger.Table][trigger.Name] = triggerContract{event: trigger.Event, statement: trigger.Statement}
	}
	for _, name := range tables {
		events := []struct {
			suffix string
			event  string
		}{{"_audit_bi", "INSERT"}, {"_audit_bu", "UPDATE"}, {"_audit_bd", "DELETE"}}
		if name == "operation_logs" || name == "security_logs" {
			// Only the reviewed retention worker may physically purge these logs.
			events = events[:2]
		}
		for _, expected := range events {
			triggerName := name + expected.suffix
			contract, ok := covered[name][triggerName]
			if !ok {
				t.Errorf("migrated table %s is missing audit trigger %s", name, triggerName)
				continue
			}
			if !strings.EqualFold(contract.event, expected.event) {
				t.Errorf("migrated table %s trigger %s fires on %s, want %s", name, triggerName, contract.event, expected.event)
			}
			if expected.event == "DELETE" && !strings.Contains(strings.ToUpper(contract.statement), "SIGNAL SQLSTATE '45000'") {
				t.Errorf("migrated table %s delete trigger %s does not reject physical deletion", name, triggerName)
			}
		}
	}
}
