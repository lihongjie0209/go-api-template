package eventbus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrRepairInvalid  = errors.New("invalid outbox repair request")
	ErrRepairConflict = errors.New("outbox event is not replayable or its version is stale")
)

type DeadRecord struct {
	ID        string    `db:"id" json:"id"`
	Subject   string    `db:"subject" json:"subject"`
	Attempts  int64     `db:"attempts" json:"attempts"`
	LastError string    `db:"last_error" json:"last_error"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	DeadAt    time.Time `db:"dead_at" json:"dead_at"`
	Version   int64     `db:"version" json:"version"`
}

type DeadPage = pagination.Result[DeadRecord]

type RepairService struct {
	db         *sqlx.DB
	transactor *database.Transactor
}

func NewRepairService(db *sqlx.DB, transactor *database.Transactor) *RepairService {
	return &RepairService{db: db, transactor: transactor}
}

func (s *RepairService) Dead(ctx context.Context, request pagination.Request) (DeadPage, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return DeadPage{}, err
	}
	if actor.Type != platformprincipal.TypeSystem {
		return DeadPage{}, ErrRepairInvalid
	}
	request, err = pagination.Normalize(request)
	if err != nil || request.PageSize > 100 || len(request.Keyword) > 256 {
		return DeadPage{}, ErrRepairInvalid
	}
	where := "dead_at IS NOT NULL AND published_at IS NULL AND deleted_at IS NULL"
	args := []any{}
	if keyword := strings.TrimSpace(request.Keyword); keyword != "" {
		where += " AND (LOWER(id) LIKE ? OR LOWER(subject) LIKE ? OR LOWER(last_error) LIKE ?)"
		pattern := "%" + strings.ToLower(keyword) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind("SELECT count(*) FROM event_outbox WHERE "+where), args...); err != nil {
		return DeadPage{}, fmt.Errorf("count dead outbox events: %w", err)
	}
	items := []DeadRecord{}
	query := s.db.Rebind("SELECT id,subject,attempts,last_error,created_at,dead_at,version FROM event_outbox WHERE " + where + " ORDER BY dead_at DESC,id LIMIT ? OFFSET ?")
	pageArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	if err := s.db.SelectContext(ctx, &items, query, pageArgs...); err != nil {
		return DeadPage{}, fmt.Errorf("list dead outbox events: %w", err)
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	for index := range items {
		items[index].CreatedAt = items[index].CreatedAt.In(location)
		items[index].DeadAt = items[index].DeadAt.In(location)
	}
	return DeadPage{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *RepairService) Replay(ctx context.Context, id string, version int64) error {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return err
	}
	if actor.Type != platformprincipal.TypeSystem {
		return ErrRepairInvalid
	}
	if strings.TrimSpace(id) == "" || len(id) > 64 || version <= 0 {
		return ErrRepairInvalid
	}
	return s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		query := tx.Rebind("UPDATE event_outbox SET attempts=0,available_at=?,dead_at=NULL,last_error='',locked_by='',locked_until=NULL,updated_at=?,version=version+1 WHERE id=? AND version=? AND dead_at IS NOT NULL AND published_at IS NULL AND deleted_at IS NULL")
		result, err := tx.ExecContext(ctx, query, now, now, id, version)
		if err != nil {
			return fmt.Errorf("replay dead outbox event: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("replay dead outbox affected rows: %w", err)
		}
		if rows != 1 {
			return ErrRepairConflict
		}
		return nil
	})
}
