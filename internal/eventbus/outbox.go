package eventbus

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
)

// Outbox atomically stores event envelopes with domain writes and dispatches
// only committed rows. JetStream de-duplicates retries by envelope event ID.
type Outbox struct {
	cfg        config.EventBus
	appName    string
	bus        *Bus
	transactor *database.Transactor
	metrics    *observability.Metrics
	logger     *slog.Logger
	workerID   string
}

type outboxRecord struct {
	ID          string    `db:"id"`
	Subject     string    `db:"subject"`
	Envelope    []byte    `db:"envelope"`
	TraceParent string    `db:"trace_parent"`
	TraceState  string    `db:"trace_state"`
	Attempts    int64     `db:"attempts"`
	Created     time.Time `db:"created_at"`
}

const maxOutboxEnvelopeBytes = 1 << 20

var outboxTracer = otel.Tracer("go-api-template/infrastructure/eventbus")

func NewOutbox(cfg config.Config, bus *Bus, transactor *database.Transactor, metrics *observability.Metrics, logger *slog.Logger) *Outbox {
	return &Outbox{
		cfg:        cfg.EventBus,
		appName:    cfg.App.Name,
		bus:        bus,
		transactor: transactor,
		metrics:    metrics,
		logger:     logger,
		workerID:   cfg.App.Name + ":" + uuid.NewString(),
	}
}

// Store persists an envelope through a caller-owned transaction. It never
// publishes directly, so rollback of the domain transaction also removes the
// pending event.
func (o *Outbox) Store(ctx context.Context, tx *sqlx.Tx, subject string, envelope *commonv1.EventEnvelope) error {
	if o == nil || !o.cfg.Enabled || o.bus == nil {
		return errors.New("transactional outbox is unavailable")
	}
	if tx == nil || strings.TrimSpace(subject) == "" || len(subject) > 256 || envelope == nil || envelope.EventId == "" {
		return errors.New("transaction, subject and event envelope are required")
	}
	data, err := proto.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode outbox envelope: %w", err)
	}
	if len(data) > maxOutboxEnvelopeBytes {
		return fmt.Errorf("outbox envelope exceeds %d bytes", maxOutboxEnvelopeBytes)
	}
	actor, ok := platformprincipal.FromContext(ctx)
	if !ok || actor.ID == "" {
		return platformprincipal.ErrMissing
	}
	now := time.Now()
	traceParent, traceState := injectTraceContext(ctx)
	query := tx.Rebind(`INSERT INTO event_outbox (id,subject,envelope,trace_parent,trace_state,available_at,attempts,last_error,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,0,'',?,?,?,?,1)`)
	if _, err := tx.ExecContext(ctx, query, envelope.EventId, subject, data, traceParent, traceState, now, now, actor.ID, now, actor.ID); err != nil {
		return fmt.Errorf("store outbox event: %w", err)
	}
	return nil
}

// Run dispatches pending rows until ctx is cancelled.
func (o *Outbox) Run(ctx context.Context) {
	if o == nil || !o.cfg.Enabled || o.bus == nil {
		return
	}
	ticker := time.NewTicker(o.cfg.DispatchInterval)
	defer ticker.Stop()
	for {
		if err := o.dispatchBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
			o.logger.ErrorContext(ctx, "dispatch transactional outbox", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (o *Outbox) dispatchBatch(ctx context.Context) error {
	for range o.cfg.DispatchBatchSize {
		record, found, err := o.claim(ctx)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		started := time.Now()
		dispatchCtx, span := outboxTracer.Start(extractTraceContext(ctx, record.TraceParent, record.TraceState), "event_outbox.dispatch", trace.WithSpanKind(trace.SpanKindProducer), trace.WithAttributes(attribute.String("messaging.system", "nats"), attribute.String("messaging.destination.name", record.Subject)))
		var envelope commonv1.EventEnvelope
		if err := proto.Unmarshal(record.Envelope, &envelope); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "decode envelope")
			if releaseErr := o.release(dispatchCtx, record.ID, record.Attempts, fmt.Errorf("decode envelope: %w", err)); releaseErr != nil {
				span.RecordError(releaseErr)
				span.End()
				return errors.Join(err, releaseErr)
			}
			span.End()
			continue
		}
		err = Publish(dispatchCtx, o.bus, record.Subject, &envelope)
		status := "success"
		if err != nil {
			status = "error"
			span.RecordError(err)
			span.SetStatus(codes.Error, "publish failed")
		}
		o.metrics.ObserveInfrastructure("event_outbox", "jetstream", "dispatch", status, started)
		if err != nil {
			o.logger.WarnContext(dispatchCtx, "outbox event publish failed", "event_id", record.ID, "attempt", record.Attempts+1, "dead", record.Attempts+1 >= int64(o.cfg.ConsumerMaxDeliver), "error", err)
			if releaseErr := o.release(dispatchCtx, record.ID, record.Attempts, err); releaseErr != nil {
				span.RecordError(releaseErr)
				span.End()
				return errors.Join(err, releaseErr)
			}
			span.End()
			continue
		}
		if err := o.markPublished(dispatchCtx, record.ID); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "mark published")
			span.End()
			return err
		}
		span.End()
	}
	return nil
}

func (o *Outbox) claim(ctx context.Context) (outboxRecord, bool, error) {
	actorCtx := platformprincipal.SystemContext(ctx, o.appName+":outbox-dispatcher")
	var record outboxRecord
	err := o.transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		query := tx.Rebind(`SELECT id,subject,envelope,trace_parent,trace_state,attempts,created_at FROM event_outbox WHERE published_at IS NULL AND dead_at IS NULL AND deleted_at IS NULL AND available_at<=? AND (locked_until IS NULL OR locked_until<?) ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`)
		now := time.Now()
		if err := tx.GetContext(actorCtx, &record, query, now, now); err != nil {
			return err
		}
		result, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE event_outbox SET locked_by=?,locked_until=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND published_at IS NULL AND deleted_at IS NULL`), o.workerID, now.Add(o.cfg.DispatchLease), now, o.appName+":outbox-dispatcher", record.ID)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("claim outbox affected rows: %w", err)
		}
		if rows != 1 {
			return errors.New("outbox claim was lost")
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return outboxRecord{}, false, nil
	}
	if err != nil {
		return outboxRecord{}, false, fmt.Errorf("claim outbox event: %w", err)
	}
	return record, true, nil
}

func injectTraceContext(ctx context.Context) (string, string) {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get("traceparent"), carrier.Get("tracestate")
}

func extractTraceContext(ctx context.Context, traceParent, traceState string) context.Context {
	carrier := propagation.MapCarrier{"traceparent": traceParent, "tracestate": traceState}
	return propagation.TraceContext{}.Extract(ctx, carrier)
}

func (o *Outbox) markPublished(ctx context.Context, id string) error {
	return o.updateClaim(ctx, id, true, 0, nil)
}

func (o *Outbox) release(ctx context.Context, id string, attempts int64, cause error) error {
	return o.updateClaim(ctx, id, false, attempts, cause)
}

func (o *Outbox) updateClaim(ctx context.Context, id string, published bool, attempts int64, cause error) error {
	actorID := o.appName + ":outbox-dispatcher"
	actorCtx := platformprincipal.SystemContext(ctx, actorID)
	return o.transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		var query string
		var args []any
		if published {
			query = `UPDATE event_outbox SET published_at=?,locked_by='',locked_until=NULL,last_error='',updated_at=?,updated_by=?,version=version+1 WHERE id=? AND locked_by=? AND published_at IS NULL AND deleted_at IS NULL`
			args = []any{now, now, actorID, id, o.workerID}
		} else {
			message := "unknown publish failure"
			if cause != nil {
				message = cause.Error()
			}
			if len(message) > 2048 {
				message = message[:2048]
			}
			if attempts+1 >= int64(o.cfg.ConsumerMaxDeliver) {
				query = `UPDATE event_outbox SET attempts=attempts+1,dead_at=?,locked_by='',locked_until=NULL,last_error=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND locked_by=? AND published_at IS NULL AND deleted_at IS NULL`
				args = []any{now, message, now, actorID, id, o.workerID}
			} else {
				query = `UPDATE event_outbox SET attempts=attempts+1,available_at=?,locked_by='',locked_until=NULL,last_error=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND locked_by=? AND published_at IS NULL AND deleted_at IS NULL`
				args = []any{now.Add(o.cfg.DispatchRetryDelay), message, now, actorID, id, o.workerID}
			}
		}
		result, err := tx.ExecContext(actorCtx, tx.Rebind(query), args...)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("update outbox claim affected rows: %w", err)
		}
		if rows != 1 {
			return errors.New("outbox lease was lost")
		}
		return nil
	})
}
