package eventbus

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestRepairServiceDeadScopesAndBoundsPage(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := NewRepairService(db, database.NewTransactor(db))
	ctx := platformprincipal.SystemContext(t.Context(), "operator")
	created := time.Now().UTC().Add(-time.Hour)
	dead := time.Now().UTC()
	mock.ExpectQuery(`SELECT count\(\*\) FROM event_outbox WHERE dead_at IS NOT NULL`).
		WithArgs("%publish%", "%publish%", "%publish%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT id,subject,attempts,last_error,created_at,dead_at,version FROM event_outbox WHERE dead_at IS NOT NULL`).
		WithArgs("%publish%", "%publish%", "%publish%", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "subject", "attempts", "last_error", "created_at", "dead_at", "version"}).
			AddRow("event-1", "platform.event.v1", 10, "publish failed", created, dead, 3))

	page, err := service.Dead(ctx, pagination.Request{Keyword: " Publish "})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, "event-1", page.Items[0].ID)
	_, offset := page.Items[0].DeadAt.Zone()
	require.Equal(t, 8*60*60, offset)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepairServiceReplayUsesAuditActorAndVersion(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := NewRepairService(db, database.NewTransactor(db))
	ctx := platformprincipal.SystemContext(t.Context(), "operator")
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("operator").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE event_outbox SET attempts=0`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "event-1", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, service.Replay(ctx, "event-1", 3))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepairServiceRejectsMissingActorAndInvalidReplay(t *testing.T) {
	service := NewRepairService(nil, &database.Transactor{})
	_, err := service.Dead(t.Context(), pagination.Request{})
	require.Error(t, err)
	ctx := platformprincipal.SystemContext(t.Context(), "operator")
	require.ErrorIs(t, service.Replay(ctx, "", 0), ErrRepairInvalid)
}
