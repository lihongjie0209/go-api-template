package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
)

func TestTenantVersionMissIsDistinctFromUniqueConflict(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(context.Context, *sqlx.Tx) error
	}{
		{"update", func(ctx context.Context, tx *sqlx.Tx) error {
			return updateTenant(ctx, tx, "tenant-1", "name", "", StatusActive, 2, "actor-1")
		}},
		{"delete", func(ctx context.Context, tx *sqlx.Tx) error {
			return deleteTenant(ctx, tx, "tenant-1", 2, "actor-1")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			db := sqlx.NewDb(raw, "sqlmock")
			mock.ExpectBegin()
			if test.name == "delete" {
				mock.ExpectExec("UPDATE application_session_contexts SET deleted_at").
					WithArgs(sqlmock.AnyArg(), "actor-1", sqlmock.AnyArg(), "actor-1", "tenant-1").
					WillReturnResult(sqlmock.NewResult(0, 1))
				for range 9 {
					mock.ExpectExec("UPDATE tenant_").WillReturnResult(sqlmock.NewResult(0, 0))
				}
			}
			mock.ExpectExec("UPDATE tenants SET").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectRollback()
			tx, err := db.Beginx()
			if err != nil {
				t.Fatal(err)
			}
			err = test.run(context.Background(), tx)
			if !errors.Is(err, ErrStaleVersion) || errors.Is(err, ErrConflict) {
				t.Fatalf("version miss = %v, want only ErrStaleVersion", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeleteTenantStopsWhenApplicationContextCleanupFails(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE application_session_contexts SET deleted_at").
		WithArgs(sqlmock.AnyArg(), "actor-1", sqlmock.AnyArg(), "actor-1", "tenant-1").
		WillReturnError(errors.New("context cleanup unavailable"))
	mock.ExpectRollback()
	tx, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteTenant(context.Background(), tx, "tenant-1", 2, "actor-1"); err == nil {
		t.Fatal("delete succeeded after application context cleanup failed")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
