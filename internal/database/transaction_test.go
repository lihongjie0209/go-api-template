package database

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

func TestTransactor_WithinSetsPostgresAuditActor(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("user-42").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-42", Type: platformprincipal.TypeUser})

	if err := NewTransactor(db).Within(ctx, nil, func(*sqlx.Tx) error { return nil }); err != nil {
		t.Fatalf("Within() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTransactor_WithinRejectsMissingAuditActor(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	mock.ExpectBegin()
	mock.ExpectRollback()

	err = NewTransactor(db).Within(t.Context(), nil, func(*sqlx.Tx) error { return nil })
	if !errors.Is(err, ErrMissingAuditActor) {
		t.Fatalf("Within() error = %v, want ErrMissingAuditActor", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
