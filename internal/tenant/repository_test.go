package tenant

import (
	"errors"
	"testing"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

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
