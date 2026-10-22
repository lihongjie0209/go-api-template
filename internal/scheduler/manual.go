package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"go.opentelemetry.io/otel"
)

const manualDispatchEventType = "platform.scheduled-job.triggered.v1"
const manualLeaseDuration = time.Minute

type manualDispatchPayload struct {
	RunID string `json:"run_id"`
}

type runIDContextKey struct{}

// RunIDFromContext returns the durable ID for an asynchronously triggered run.
// Handlers with external side effects can use it as a business idempotency key.
func RunIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(runIDContextKey{}).(string)
	return id, ok && id != ""
}

type manualRun struct {
	ID             string `db:"id"`
	ScheduledJobID string `db:"scheduled_job_id"`
	JobCode        string `db:"job_code"`
	JobName        string `db:"job_name"`
	Handler        string `db:"handler"`
	Payload        string `db:"payload"`
	TimeoutSeconds int64  `db:"timeout_seconds"`
	LockTTLSeconds int64  `db:"lock_ttl_seconds"`
	RequestID      string `db:"request_id"`
	TraceParent    string `db:"trace_parent"`
	TraceState     string `db:"trace_state"`
	LeaseOwner     string `db:"-"`
}

func (m *Manager) NotifyManual() {
	select {
	case m.manualWake <- struct{}{}:
	default:
	}
}

func (m *Manager) consumeManual(ctx context.Context, event *commonv1.EventEnvelope) error {
	if event == nil || event.EventType != manualDispatchEventType || event.SchemaVersion != 1 {
		return errors.New("invalid scheduled job dispatch event")
	}
	var payload manualDispatchPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("decode scheduled job dispatch: %w", err)
	}
	if payload.RunID == "" || payload.RunID != event.EventId || event.AggregateId == "" {
		return errors.New("scheduled job dispatch ID mismatch")
	}
	actorCtx := platformprincipal.SystemContext(ctx, m.cfg.App.Name+":scheduler:dispatcher")
	if err := m.tx.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		result, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE scheduled_job_runs SET dispatched_at=?,updated_at=?,updated_by=? WHERE id=? AND scheduled_job_id=? AND trigger_source='manual' AND status='queued' AND dispatched_at IS NULL AND deleted_at IS NULL`), now, now, m.cfg.App.Name+":scheduler:dispatcher", payload.RunID, event.AggregateId)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			var id string
			if err := tx.GetContext(actorCtx, &id, tx.Rebind(`SELECT id FROM scheduled_job_runs WHERE id=? AND scheduled_job_id=? AND trigger_source='manual' AND deleted_at IS NULL`), payload.RunID, event.AggregateId); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("mark manual scheduled job dispatched: %w", err)
	}
	m.NotifyManual()
	return nil
}

func (m *Manager) manualLoop(ctx context.Context) {
	if !m.cfg.Database.Enabled {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	preferExpired := true
	for {
		if ctx.Err() != nil {
			return
		}
		run, found, err := m.claimManual(ctx, preferExpired)
		if err != nil && !errors.Is(err, context.Canceled) {
			m.logOperationFailure(ctx, "claim manual scheduled job", err)
		}
		if found {
			preferExpired = !preferExpired
			m.executeManual(ctx, run)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-m.manualWake:
		case <-ticker.C:
		}
	}
}

func (m *Manager) claimManual(ctx context.Context, preferExpired bool) (manualRun, bool, error) {
	actorCtx := platformprincipal.SystemContext(ctx, m.cfg.App.Name+":scheduler:manual")
	var run manualRun
	workerID := uuid.NewString()
	err := m.tx.Within(actorCtx, &sql.TxOptions{}, func(tx *sqlx.Tx) error {
		now := time.Now()
		const columns = `id,scheduled_job_id,job_code,job_name,handler,payload,timeout_seconds,lock_ttl_seconds,request_id,trace_parent,trace_state`
		loadQueued := func() error {
			query := tx.Rebind(`SELECT ` + columns + ` FROM scheduled_job_runs WHERE trigger_source='manual' AND status='queued' AND dispatched_at IS NOT NULL AND deleted_at IS NULL ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`)
			return tx.GetContext(actorCtx, &run, query)
		}
		loadExpired := func() error {
			query := tx.Rebind(`SELECT ` + columns + ` FROM scheduled_job_runs WHERE trigger_source='manual' AND status='running' AND lease_until<? AND deleted_at IS NULL ORDER BY lease_until,id LIMIT 1 FOR UPDATE SKIP LOCKED`)
			return tx.GetContext(actorCtx, &run, query, now)
		}
		first, second := loadQueued, loadExpired
		if preferExpired {
			first, second = second, first
		}
		if err := first(); errors.Is(err, sql.ErrNoRows) {
			if err := second(); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		result, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE scheduled_job_runs SET status='running',started_at=?,finished_at=NULL,lease_owner=?,lease_until=?,updated_at=?,updated_by=? WHERE id=? AND trigger_source='manual' AND deleted_at IS NULL AND ((status='queued' AND dispatched_at IS NOT NULL) OR (status='running' AND lease_until<?))`), now, workerID, now.Add(manualLeaseDuration), now, m.cfg.App.Name+":scheduler:manual", run.ID, now)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("manual scheduled job claim was lost")
		}
		run.LeaseOwner = workerID
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return manualRun{}, false, nil
	}
	return run, err == nil, err
}

func (m *Manager) executeManual(parent context.Context, run manualRun) {
	started := time.Now()
	traceCtx := observability.RestoreTraceContext(parent, run.TraceParent, run.TraceState)
	ctx, cancel := context.WithTimeout(traceCtx, time.Duration(run.TimeoutSeconds)*time.Second)
	ctx = requestid.WithContext(platformprincipal.SystemContext(ctx, m.cfg.App.Name+":scheduler:manual"), run.RequestID)
	ctx = context.WithValue(ctx, runIDContextKey{}, run.ID)
	ctx, span := otel.Tracer("go-api-template/scheduler").Start(ctx, "scheduled_job.manual.execute")
	defer span.End()
	defer cancel()
	done := make(chan struct{})
	renewed := make(chan struct{})
	leaseLost := false
	go func() {
		defer close(renewed)
		if err := watchManualLease(ctx, done, manualLeaseDuration/3, func(ctx context.Context) error {
			return m.renewManual(ctx, run.ID, run.LeaseOwner)
		}); err != nil {
			m.logOperationFailure(ctx, "manual scheduled job lease lost", err)
			leaseLost = true
			cancel()
		}
	}()
	handler, err := m.handlers.Resolve(run.Handler)
	if err == nil {
		err = runManualJobContext(ctx, run.JobCode, time.Duration(run.LockTTLSeconds)*time.Second, m.cfg.DistributedLock.RetryDelay, m.locker, func(ctx context.Context) error {
			return handler(ctx, json.RawMessage(run.Payload))
		}, m.logger)
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	close(done)
	<-renewed
	if err != nil {
		recordJobFailure(span, err)
	}
	if parent.Err() != nil || leaseLost {
		return // The expired lease is reclaimed after restart or by another instance.
	}
	status := statusForRun(err)
	if finishErr := m.finishManual(ctx, run, status, started); finishErr != nil {
		m.logOperationFailure(parent, "finish manual scheduled job", finishErr)
	}
	m.metrics.ObserveCron(run.Handler, status, started)
}

func watchManualLease(ctx context.Context, done <-chan struct{}, interval time.Duration, renew func(context.Context) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := renew(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

func (m *Manager) renewManual(ctx context.Context, id, owner string) error {
	actorCtx := platformprincipal.SystemContext(ctx, m.cfg.App.Name+":scheduler:manual")
	return m.tx.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		result, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE scheduled_job_runs SET lease_until=?,updated_at=?,updated_by=? WHERE id=? AND status='running' AND lease_owner=? AND lease_until>? AND deleted_at IS NULL`), now.Add(manualLeaseDuration), now, m.cfg.App.Name+":scheduler:manual", id, owner, now)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("manual scheduled job lease was lost")
		}
		return nil
	})
}

func (m *Manager) finishManual(ctx context.Context, run manualRun, status string, started time.Time) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	actorCtx := platformprincipal.SystemContext(finishCtx, m.cfg.App.Name+":scheduler:manual")
	return m.tx.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		errorMessage := ""
		if status == "error" {
			errorMessage = "scheduled job execution failed"
		}
		result, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE scheduled_job_runs SET status=?,finished_at=?,duration_ms=?,error_message=?,lease_owner='',lease_until=NULL,updated_at=?,updated_by=? WHERE id=? AND status='running' AND lease_owner=? AND lease_until>? AND deleted_at IS NULL`), status, now, max(0, now.Sub(started).Milliseconds()), errorMessage, now, m.cfg.App.Name+":scheduler:manual", run.ID, run.LeaseOwner, now)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("manual scheduled job completion lease was lost")
		}
		if m.operations != nil {
			return m.operations.RecordTx(actorCtx, tx, runCompletionEntry(run.ID, run.ScheduledJobID, run.JobCode, run.JobName, run.Handler, "manual", status, started, now))
		}
		return nil
	})
}
