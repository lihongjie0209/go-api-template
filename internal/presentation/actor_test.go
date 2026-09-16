package presentation

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
)

func TestActorNamesDeduplicatesResolvesAndFallsBack(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id,display_name FROM identity_users WHERE deleted_at IS NULL AND id IN (?, ?)
UNION ALL SELECT id,name AS display_name FROM identity_service_accounts WHERE deleted_at IS NULL AND id IN (?, ?)`)).
		WithArgs("user-1", "system-1", "user-1", "system-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "display_name"}).AddRow("user-1", "Alice"))

	names, err := ActorNames(t.Context(), db, "user-1", "system-1", "user-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if names["user-1"] != "Alice" || names["system-1"] != "system-1" || len(names) != 2 {
		t.Fatalf("names = %#v", names)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestActorNamesRejectsUnboundedInput(t *testing.T) {
	t.Parallel()
	ids := make([]string, maxActorIDs+1)
	for index := range ids {
		ids[index] = string(rune(index+1)) + "-actor"
	}
	if _, err := ActorNames(t.Context(), nil, ids...); err == nil {
		t.Fatal("ActorNames() error = nil")
	}
}
