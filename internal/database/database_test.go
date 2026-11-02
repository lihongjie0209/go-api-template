package database

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/XSAM/otelsql"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type mockSQLConnector struct {
	driver driver.Driver
	dsn    string
}

func (c mockSQLConnector) Connect(context.Context) (driver.Conn, error) { return c.driver.Open(c.dsn) }
func (c mockSQLConnector) Driver() driver.Driver                        { return c.driver }

func TestDatabaseConstructedBeforeTracingStartUsesActiveProvider(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previousProvider) })

	const dsn = "database-tracing-lifecycle-test"
	raw, mock, err := sqlmock.NewWithDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	observed := otelsql.OpenDB(mockSQLConnector{driver: raw.Driver(), dsn: dsn})
	t.Cleanup(func() { _ = observed.Close() })

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	otel.SetTracerProvider(provider)

	mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(1))
	var value int
	if err := observed.QueryRowContext(t.Context(), "SELECT 1").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != 1 || len(recorder.Ended()) == 0 {
		t.Fatalf("query value=%d, recorded spans=%d; want a traced query", value, len(recorder.Ended()))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

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
