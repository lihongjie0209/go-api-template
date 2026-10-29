//go:build integration

package operationlog

import (
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

func TestMySQLConsumerAcknowledgesOnlyOnePersistedEvent(t *testing.T) {
	db := testutil.MySQLDatabase(t)
	service := New(config.Config{
		App:          config.App{Name: "operation-log-integration"},
		OperationLog: config.OperationLog{Enabled: true, MaxPayloadBytes: 4096},
	}, nil, db, database.NewTransactor(db), nil, nil, nil)
	ctx := platformprincipal.SystemContext(t.Context(), "audit-test")
	event, err := service.envelope(ctx, Entry{Operation: "audit.integration", Source: "backend", Protocol: "service", Succeeded: true})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := service.consume(ctx, event); err != nil {
			t.Fatalf("consume event twice: %v", err)
		}
	}
	var stored struct {
		Count   int    `db:"row_count"`
		Version int64  `db:"version"`
		Actor   string `db:"created_by"`
	}
	if err := db.GetContext(ctx, &stored, `SELECT count(*) AS row_count, max(version) AS version, max(created_by) AS created_by FROM operation_logs WHERE id=?`, event.EventId); err != nil {
		t.Fatal(err)
	}
	if stored.Count != 1 || stored.Version != 1 || stored.Actor != "audit-test" {
		t.Fatalf("persisted duplicate event = %+v", stored)
	}
}
