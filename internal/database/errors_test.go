package database

import (
	"errors"
	"testing"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestIsUniqueViolation(t *testing.T) {
	require.True(t, IsUniqueViolation(&pgconn.PgError{Code: "23505"}))
	require.True(t, IsUniqueViolation(errors.Join(errors.New("insert"), &mysqlDriver.MySQLError{Number: 1062})))
	require.False(t, IsUniqueViolation(&pgconn.PgError{Code: "23503"}))
	require.False(t, IsUniqueViolation(errors.New("database unavailable")))
}

func TestIsRetryableTransaction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "postgres serialization", err: &pgconn.PgError{Code: "40001"}, want: true},
		{name: "postgres wrapped deadlock", err: errors.Join(errors.New("commit failed"), &pgconn.PgError{Code: "40P01"}), want: true},
		{name: "mysql deadlock", err: &mysqlDriver.MySQLError{Number: 1213}, want: true},
		{name: "postgres unique", err: &pgconn.PgError{Code: "23505"}},
		{name: "mysql lock timeout", err: &mysqlDriver.MySQLError{Number: 1205}},
		{name: "unavailable", err: errors.New("unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, IsRetryableTransaction(tt.err))
		})
	}
}
