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

func TestMigrationTablesFollowAuditContract(t *testing.T) {
	t.Parallel()
	for _, dialect := range []string{"postgres", "kingbase", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			files, err := filepath.Glob(filepath.Join("..", "..", "migrations", dialect, "*.up.sql"))
			if err != nil {
				t.Fatal(err)
			}
			var allDDL strings.Builder
			for _, file := range files {
				content, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateAuditDDL(string(content), dialect); err != nil {
					t.Errorf("%s: %v", file, err)
				}
				allDDL.Write(content)
				allDDL.WriteByte('\n')
			}
			if dialect == "mysql" {
				if err := validateMySQLAuditTriggers(allDDL.String()); err != nil {
					t.Error(err)
				}
			}
		})
	}
}

func TestMySQLDownMigrationsDoNotDropIndexesFromRetiredRoutePolicyTables(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "mysql", "*.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(content))
		if regexp.MustCompile(`drop\s+index\s+\S+\s+on\s+route_policy_permission_refs`).MatchString(lower) {
			t.Errorf("%s drops an index from a table irreversibly retired by migration 000026", file)
		}
	}
}

func TestMySQLDataMigrationsSetAndClearAuditActor(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "mysql", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	mutation := regexp.MustCompile(`(?im)^\s*(?:insert\s+into|update)\s+`)
	for _, file := range files {
		// MySQL audit triggers were introduced by migration 000019. Earlier
		// data migrations cannot populate the transaction actor variable.
		if filepath.Base(file) < "000019" {
			continue
		}
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !mutation.Match(content) {
			continue
		}
		sql := strings.ToLower(string(content))
		if !strings.Contains(sql, "set @app_actor_id = 'system:migration'") ||
			!strings.Contains(sql, "set @app_actor_id = null") {
			t.Errorf("%s mutates audited data without setting and clearing the migration actor", file)
		}
	}
}

func validateMySQLAuditTriggers(sql string) error {
	lower := strings.ToLower(sql)
	physicalDeleteAllowed := map[string]bool{"operation_logs": true, "security_logs": true}
	for _, match := range createTablePattern.FindAllStringSubmatch(sql, -1) {
		table := strings.ToLower(strings.Trim(match[1], "\"`"))
		suffixes := []string{"_audit_bi before insert", "_audit_bu before update"}
		if !physicalDeleteAllowed[table] {
			suffixes = append(suffixes, "_audit_bd before delete")
		}
		for _, suffix := range suffixes {
			if !strings.Contains(lower, "create trigger "+table+suffix) {
				return fmt.Errorf("table %s is missing mysql trigger %s", table, suffix)
			}
		}
	}
	return nil
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
		timestampType := `timestamptz`
		if dialect == "mysql" {
			timestampType = `timestamp\s*\(\s*6\s*\)`
		}
		required := map[string]string{
			"created_at": `\bcreated_at\s+` + timestampType + `\s+not\s+null`,
			"created_by": `\bcreated_by\s+text\s+not\s+null`,
			"updated_at": `\bupdated_at\s+` + timestampType + `\s+not\s+null`,
			"updated_by": `\bupdated_by\s+text\s+not\s+null`,
			"version":    `\bversion\s+bigint\s+not\s+null`,
			"deleted_at": `\bdeleted_at\s+` + timestampType + `(?:\s+null)?(?:\s*,|\s*$)`,
			"deleted_by": `\bdeleted_by\s+text(?:\s+null)?(?:\s*,|\s*$)`,
		}
		for column, pattern := range required {
			if !regexp.MustCompile(`(?m)` + pattern).MatchString(definition) {
				return fmt.Errorf("table %s has a missing or invalid audit column %s", table, column)
			}
		}
		if (dialect == "postgres" || dialect == "kingbase") && !strings.Contains(strings.ToLower(sql), "app_enable_audit('"+strings.ToLower(table)+"')") {
			return fmt.Errorf("table %s is missing app_enable_audit registration", table)
		}
	}
	return nil
}
