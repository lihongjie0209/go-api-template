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
	"google.golang.org/protobuf/proto"
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
		WithArgs("%platform%", "%platform%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT id,subject,attempts,CASE WHEN OCTET_LENGTH\(last_error\) <= 14 AND last_error IN \('', 'decode_failed', 'publish_failed'\) THEN last_error ELSE 'legacy_failure' END AS last_error,created_at,dead_at,version FROM event_outbox WHERE dead_at IS NOT NULL`).
		WithArgs("%platform%", "%platform%", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "subject", "attempts", "last_error", "created_at", "dead_at", "version"}).
			AddRow("event-1", "platform.event.v1", 10, "publish failed: bearer secret-value", created, dead, 3))

	page, err := service.Dead(ctx, pagination.Request{Keyword: " Platform "})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, "event-1", page.Items[0].ID)
	require.Equal(t, "legacy_failure", page.Items[0].LastError)
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
	envelope, err := proto.Marshal(completeOutboxEnvelope("event-1"))
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("operator").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,subject,envelope FROM event_outbox`).WithArgs("event-1", int64(3), maxOutboxEnvelopeBytes).
		WillReturnRows(sqlmock.NewRows([]string{"id", "subject", "envelope"}).AddRow("event-1", "platform.event.v1", envelope))
	mock.ExpectExec(`UPDATE event_outbox SET attempts=0`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "event-1", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, service.Replay(ctx, "event-1", 3))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepairServiceReplayAcceptsValidDeadEvent(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := NewRepairService(db, database.NewTransactor(db))
	ctx := platformprincipal.SystemContext(t.Context(), "operator")
	envelope, err := proto.Marshal(completeOutboxEnvelope("event-1"))
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("operator").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,subject,envelope FROM event_outbox`).WithArgs("event-1", int64(3), maxOutboxEnvelopeBytes).
		WillReturnRows(sqlmock.NewRows([]string{"id", "subject", "envelope"}).AddRow("event-1", "platform.event.v1", envelope))
	mock.ExpectExec(`UPDATE event_outbox SET attempts=0.*AND deleted_at IS NULL$`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "event-1", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, service.Replay(ctx, "event-1", 3))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepairServiceReplaceAndReplayCorrectsDeadEvent(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := NewRepairService(db, database.NewTransactor(db))
	ctx := platformprincipal.SystemContext(t.Context(), "operator")
	envelope, err := proto.Marshal(completeOutboxEnvelope("event-1"))
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("operator").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,subject FROM event_outbox.*FOR UPDATE$`).WithArgs("event-1", int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "subject"}).AddRow("event-1", "platform.event.v1"))
	mock.ExpectExec(`UPDATE event_outbox SET envelope=\$1,attempts=0.*AND deleted_at IS NULL$`).
		WithArgs(envelope, sqlmock.AnyArg(), sqlmock.AnyArg(), "event-1", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, service.ReplaceAndReplay(ctx, "event-1", 3, envelope))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepairServiceReplaceAndReplayRejectsMismatchedEvent(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := NewRepairService(db, database.NewTransactor(db))
	ctx := platformprincipal.SystemContext(t.Context(), "operator")
	envelope := completeOutboxEnvelope("event-1")
	envelope.EventType = "platform.other.v1"
	data, err := proto.Marshal(envelope)
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("operator").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,subject FROM event_outbox.*FOR UPDATE$`).WithArgs("event-1", int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "subject"}).AddRow("event-1", "platform.event.v1"))
	mock.ExpectRollback()
	require.ErrorIs(t, service.ReplaceAndReplay(ctx, "event-1", 3, data), ErrRepairInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepairServiceReplayRejectsCorruptLegacyEnvelope(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := NewRepairService(db, database.NewTransactor(db))
	ctx := platformprincipal.SystemContext(t.Context(), "operator")
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("operator").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,subject,envelope FROM event_outbox`).WithArgs("event-1", int64(3), maxOutboxEnvelopeBytes).
		WillReturnRows(sqlmock.NewRows([]string{"id", "subject", "envelope"}).AddRow("event-1", "platform.event.v1", []byte("corrupt")))
	mock.ExpectRollback()
	require.ErrorIs(t, service.Replay(ctx, "event-1", 3), ErrRepairConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepairServiceReplayRejectsOversizedEnvelopeBeforeLoading(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := NewRepairService(db, database.NewTransactor(db))
	ctx := platformprincipal.SystemContext(t.Context(), "operator")
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.actor_id', \$1, true\)`).WithArgs("operator").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,subject,envelope FROM event_outbox.*OCTET_LENGTH\(envelope\)<=\$3 FOR UPDATE`).
		WithArgs("event-1", int64(3), maxOutboxEnvelopeBytes).
		WillReturnRows(sqlmock.NewRows([]string{"id", "subject", "envelope"}))
	mock.ExpectRollback()
	require.ErrorIs(t, service.Replay(ctx, "event-1", 3), ErrRepairConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSafeOutboxReason(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, input, want string }{
		{name: "empty", input: "", want: ""},
		{name: "decode", input: "decode_failed", want: "decode_failed"},
		{name: "publish", input: "publish_failed", want: "publish_failed"},
		{name: "legacy sensitive error", input: "publish failed: bearer secret-value", want: "legacy_failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.want, safeOutboxReason(test.input))
		})
	}
}

func TestRepairServiceRejectsMissingActorAndInvalidReplay(t *testing.T) {
	service := NewRepairService(nil, &database.Transactor{})
	_, err := service.Dead(t.Context(), pagination.Request{})
	require.Error(t, err)
	ctx := platformprincipal.SystemContext(t.Context(), "operator")
	require.ErrorIs(t, service.Replay(ctx, "", 0), ErrRepairInvalid)
	require.ErrorIs(t, service.ReplaceAndReplay(ctx, "event-1", 1, nil), ErrRepairInvalid)
	require.ErrorIs(t, service.ReplaceAndReplay(ctx, "event-1", 1, make([]byte, maxOutboxEnvelopeBytes+1)), ErrRepairInvalid)
}
