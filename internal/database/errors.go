package database

import (
	"errors"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// IsUniqueViolation recognizes duplicate-key errors from every supported SQL
// driver. Business modules should translate this infrastructure condition to
// their own stable conflict error.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

// IsRetryableTransaction reports transaction-abort errors for which the whole
// operation may be retried after the transaction has rolled back.
func IsRetryableTransaction(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1213
}
