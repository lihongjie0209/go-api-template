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
