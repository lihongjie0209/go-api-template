package identity

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

func TestSecurityTargetUsesUncachedMinimalQuery(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	id := "d5298761-7e87-4393-b660-8d0c939a570b"
	mock.ExpectQuery(`SELECT id,username,display_name,status FROM identity_users WHERE id=\? AND status<>'closed' AND deleted_at IS NULL`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status"}).AddRow(id, "alice", "Alice", "disabled"))
	service := &Service{repository: NewRepository(sqlx.NewDb(raw, "sqlmock"))}
	ctx := platformprincipal.SystemContext(t.Context(), "tester")
	target, err := service.SecurityTarget(ctx, id)
	if err != nil || target.ID != id || target.Status != StatusDisabled {
		t.Fatalf("SecurityTarget()=%+v, %v", target, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityTargetValidationAndMissing(t *testing.T) {
	t.Parallel()
	service := &Service{}
	if _, err := service.SecurityTarget(t.Context(), "d5298761-7e87-4393-b660-8d0c939a570b"); err == nil {
		t.Fatal("missing actor accepted")
	}
	ctx := platformprincipal.SystemContext(t.Context(), "tester")
	for _, id := range []string{"", "not-uuid", "D5298761-7E87-4393-B660-8D0C939A570B"} {
		if _, err := service.SecurityTarget(ctx, id); !errors.Is(err, ErrInvalid) {
			t.Fatalf("id %q: %v", id, err)
		}
	}
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT id,username,display_name,status FROM identity_users WHERE id=\? AND status<>'closed' AND deleted_at IS NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status"}))
	service.repository = NewRepository(sqlx.NewDb(raw, "sqlmock"))
	if _, err := service.SecurityTarget(ctx, "d5298761-7e87-4393-b660-8d0c939a570b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing target: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteTargetUsesUncachedVersionedProjection(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	id := "d5298761-7e87-4393-b660-8d0c939a570b"
	mock.ExpectQuery(`SELECT id,username,display_name,status,version FROM identity_users WHERE id=\? AND status<>'closed' AND deleted_at IS NULL`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status", "version"}).AddRow(id, "alice", "Alice", "locked", 7))
	service := &Service{repository: NewRepository(sqlx.NewDb(raw, "sqlmock"))}
	target, err := service.DeleteTarget(platformprincipal.SystemContext(t.Context(), "tester"), id)
	if err != nil || target.ID != id || target.Version != 7 || target.Status != StatusLocked {
		t.Fatalf("DeleteTarget()=%+v, %v", target, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteTargetValidationMissingAndFailure(t *testing.T) {
	t.Parallel()
	service := &Service{}
	id := "d5298761-7e87-4393-b660-8d0c939a570b"
	if _, err := service.DeleteTarget(t.Context(), id); err == nil {
		t.Fatal("missing actor accepted")
	}
	ctx := platformprincipal.SystemContext(t.Context(), "tester")
	for _, invalid := range []string{"", "not-uuid", "D5298761-7E87-4393-B660-8D0C939A570B"} {
		if _, err := service.DeleteTarget(ctx, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("id %q: %v", invalid, err)
		}
	}
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	service.repository = NewRepository(sqlx.NewDb(raw, "sqlmock"))
	query := `SELECT id,username,display_name,status,version FROM identity_users WHERE id=\? AND status<>'closed' AND deleted_at IS NULL`
	mock.ExpectQuery(query).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status", "version"}))
	if _, err := service.DeleteTarget(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing target: %v", err)
	}
	backendFailure := errors.New("database unavailable")
	mock.ExpectQuery(query).WithArgs(id).WillReturnError(backendFailure)
	if _, err := service.DeleteTarget(ctx, id); !errors.Is(err, backendFailure) {
		t.Fatalf("database error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
