//go:build integration

package integration

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	appdb "github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	"github.com/lihongjie0209/go-api-template/internal/migration"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestTransactionalOutboxDeliversCommittedEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	dsn, migrationURL := startDatabase(t, ctx, "postgres")
	natsURL := startNATS(t, ctx)
	migrationPath, err := filepath.Abs(filepath.Join("..", "migrations", "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	migrationCfg := config.Migration{
		Path: migrationPath, DatabaseURL: migrationURL,
		Table: "integration_eventbus_schema_migrations", Schema: "integration_eventbus", CreateSchema: true,
	}
	if err := migration.Run(migrationCfg, "up", 0); err != nil {
		t.Fatal(err)
	}
	db, err := appdb.Open(ctx, config.Database{
		Type: "postgres", DSN: dsn, Schema: migrationCfg.Schema,
		MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute, PingTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.Config{
		App: config.App{Name: "integration-eventbus"},
		EventBus: config.EventBus{
			Enabled: true, URLs: []string{natsURL}, StreamName: "INTEGRATION_EVENTS", Subjects: []string{"platform.>"}, Storage: "memory",
			MaxAge: time.Hour, DuplicateWindow: time.Minute, ConnectTimeout: 10 * time.Second, ReconnectWait: 100 * time.Millisecond,
			PublishTimeout: 5 * time.Second, ConsumerAckWait: 10 * time.Second, ConsumerHandlerTimeout: 5 * time.Second, ConsumerMaxDeliver: 3,
			DispatchInterval: 10 * time.Millisecond, DispatchBatchSize: 10, DispatchLease: 15 * time.Second, DispatchRetryDelay: 50 * time.Millisecond, DispatchRetryMaxDelay: time.Minute,
		},
	}
	bus, err := eventbus.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eventbus.Close(bus) })

	received := make(chan *commonv1.EventEnvelope, 1)
	consumerCtx, stopConsumer := context.WithCancel(ctx)
	consumerDone := make(chan error, 1)
	go func() {
		consumerDone <- bus.Consume(consumerCtx, "integration-outbox", "platform.integration.outbox.v1", func(_ context.Context, envelope *commonv1.EventEnvelope) error {
			received <- envelope
			return nil
		})
	}()
	t.Cleanup(func() {
		stopConsumer()
		<-consumerDone
	})

	transactor := appdb.NewTransactor(db)
	outbox := eventbus.NewOutbox(cfg, bus, transactor, observability.NewMetrics(config.Config{}, nil, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	envelope := &commonv1.EventEnvelope{
		EventId: "integration-event-1", EventType: "platform.integration.outbox.v1", AggregateType: "integration", AggregateId: "aggregate-1",
		SchemaVersion: 1, OccurredAt: timestamppb.Now(),
	}
	actorCtx := platformprincipal.SystemContext(ctx, "integration-eventbus:test")
	if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		return outbox.Store(actorCtx, tx, "platform.integration.outbox.v1", envelope)
	}); err != nil {
		t.Fatal(err)
	}
	dispatchCtx, stopDispatch := context.WithCancel(ctx)
	dispatchDone := make(chan struct{})
	go func() {
		defer close(dispatchDone)
		outbox.Run(dispatchCtx)
	}()
	t.Cleanup(func() {
		stopDispatch()
		<-dispatchDone
	})

	select {
	case delivered := <-received:
		if delivered.GetEventId() != envelope.GetEventId() {
			t.Fatalf("delivered event id = %q", delivered.GetEventId())
		}
	case <-ctx.Done():
		t.Fatalf("wait for outbox delivery: %v", context.Cause(ctx))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var published bool
		if err := db.GetContext(ctx, &published, "SELECT published_at IS NOT NULL FROM event_outbox WHERE id=$1", envelope.GetEventId()); err != nil {
			t.Fatal(err)
		}
		if published {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("outbox row was delivered but not marked published")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Run("dead letter invalid stored metadata", func(t *testing.T) {
		invalidID := "integration-invalid-metadata-1"
		encoded, err := proto.Marshal(&commonv1.EventEnvelope{
			EventId: invalidID, AggregateType: "integration", SchemaVersion: 1, OccurredAt: timestamppb.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		err = transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
			query := tx.Rebind(`INSERT INTO event_outbox (id,subject,envelope,trace_parent,trace_state,available_at,attempts,locked_by,last_error,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,'','',?,?,?,?,1)`)
			_, err := tx.ExecContext(actorCtx, query, invalidID, "platform.integration.outbox.v1", encoded, "", "", now, cfg.EventBus.ConsumerMaxDeliver-1, now, "integration-eventbus:test", now, "integration-eventbus:test")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			var row struct {
				DeadAt    sql.NullTime `db:"dead_at"`
				LastError string       `db:"last_error"`
				Attempts  int64        `db:"attempts"`
				Version   int64        `db:"version"`
			}
			if err := db.GetContext(ctx, &row, `SELECT dead_at,last_error,attempts,version FROM event_outbox WHERE id=$1`, invalidID); err != nil {
				t.Fatal(err)
			}
			if row.DeadAt.Valid {
				if row.LastError != "decode_failed" || row.Attempts != int64(cfg.EventBus.ConsumerMaxDeliver) {
					t.Fatalf("invalid event dead letter = %+v", row)
				}
				repair := eventbus.NewRepairService(db, transactor)
				require.ErrorIs(t, repair.Replay(actorCtx, invalidID, row.Version), eventbus.ErrRepairConflict)
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("invalid event was not dead-lettered")
			}
			time.Sleep(20 * time.Millisecond)
		}
		select {
		case event := <-received:
			if event.GetEventId() == invalidID {
				t.Fatalf("invalid event was published: %s", event.GetEventId())
			}
		default:
		}
	})
}

func startNATS(t *testing.T, ctx context.Context) string {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "nats:2.11.11-alpine",
			ExposedPorts: []string{"4222/tcp"},
			Cmd:          []string{"-js"},
			WaitingFor:   wait.ForLog("Server is ready").WithStartupTimeout(time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "4222/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return (&url.URL{Scheme: "nats", Host: host + ":" + port.Port()}).String()
}
