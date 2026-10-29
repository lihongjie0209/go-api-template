package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
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

// ConfirmDuplicateID acknowledges a unique-key conflict only when the
// requested event ID is already stored. lookupSQL must be code-owned and
// select the ID from the intended table with one bound ID argument.
func ConfirmDuplicateID(ctx context.Context, tx *sqlx.Tx, lookupSQL, id string, insertErr error) error {
	if !IsUniqueViolation(insertErr) {
		return insertErr
	}
	var storedID string
	err := tx.GetContext(ctx, &storedID, tx.Rebind(lookupSQL), id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && storedID != id {
		return insertErr
	}
	if err != nil {
		return fmt.Errorf("confirm duplicate id: %w", err)
	}
	return nil
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
