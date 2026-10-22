package database

import "testing"

func TestMySQLConnectionConfigSetsClientTimezoneAndStrictSessionMode(t *testing.T) {
	configuration, err := mysqlConnectionConfig("user:pass@tcp(localhost:3306)/old", "platform")
	if err != nil {
		t.Fatal(err)
	}
	if !configuration.ParseTime || configuration.Loc.String() != "Asia/Shanghai" || configuration.Params["time_zone"] != "'+08:00'" || configuration.Params["sql_mode"] != "CONCAT_WS(',', NULLIF(@@SESSION.sql_mode, ''), 'STRICT_TRANS_TABLES')" || configuration.DBName != "platform" {
		t.Fatalf("mysql config = %+v", configuration)
	}
}

func TestMySQLConnectionConfigPreservesCustomSQLModeBeforeEnforcingStrict(t *testing.T) {
	configuration, err := mysqlConnectionConfig("user:pass@tcp(localhost:3306)/platform?sql_mode=%27ANSI%27", "platform")
	if err != nil {
		t.Fatal(err)
	}
	if got := configuration.Params["sql_mode"]; got != "CONCAT_WS(',', NULLIF('ANSI', ''), 'STRICT_TRANS_TABLES')" {
		t.Fatalf("sql_mode = %q", got)
	}
}
