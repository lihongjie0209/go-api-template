package database

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var createTablePattern = regexp.MustCompile(`(?is)CREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?\s+([a-zA-Z0-9_."\x60]+)\s*\((.*?)\);`)

var requiredAuditColumns = []string{
	"created_at",
	"created_by",
	"updated_at",
	"updated_by",
	"version",
	"deleted_at",
	"deleted_by",
}

func TestMigrationTablesFollowAuditContract(t *testing.T) {
	t.Parallel()
	for _, dialect := range []string{"postgres", "kingbase", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			files, err := filepath.Glob(filepath.Join("..", "..", "migrations", dialect, "*.up.sql"))
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				content, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateAuditDDL(string(content), dialect); err != nil {
					t.Errorf("%s: %v", file, err)
				}
			}
		})
	}
}

func TestValidateAuditDDL(t *testing.T) {
	t.Parallel()
	valid := `CREATE TABLE memberships (
		id text PRIMARY KEY,
		created_at timestamptz NOT NULL,
		created_by text NOT NULL,
		updated_at timestamptz NOT NULL,
		updated_by text NOT NULL,
		version bigint NOT NULL,
		deleted_at timestamptz,
		deleted_by text
	);
	SELECT app_enable_audit('memberships');`
	if err := validateAuditDDL(valid, "postgres"); err != nil {
		t.Fatalf("valid DDL error = %v", err)
	}
	if err := validateAuditDDL(strings.Replace(valid, "version bigint NOT NULL,", "", 1), "postgres"); err == nil {
		t.Fatal("DDL missing version passed validation")
	}
	if err := validateAuditDDL(strings.Replace(valid, "SELECT app_enable_audit('memberships');", "", 1), "postgres"); err == nil {
		t.Fatal("DDL missing trigger registration passed validation")
	}
}

func validateAuditDDL(sql, dialect string) error {
	for _, match := range createTablePattern.FindAllStringSubmatch(sql, -1) {
		table := strings.Trim(match[1], "\"`")
		definition := strings.ToLower(match[2])
		for _, column := range requiredAuditColumns {
			if !regexp.MustCompile(`(?m)\b` + column + `\b`).MatchString(definition) {
				return fmt.Errorf("table %s is missing required audit column %s", table, column)
			}
		}
		if (dialect == "postgres" || dialect == "kingbase") && !strings.Contains(strings.ToLower(sql), "app_enable_audit('"+strings.ToLower(table)+"')") {
			return fmt.Errorf("table %s is missing app_enable_audit registration", table)
		}
	}
	return nil
}
