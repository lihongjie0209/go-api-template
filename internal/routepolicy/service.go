package routepolicy

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
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrNotFound = errors.New("route policy not found")
	ErrConflict = errors.New("route policy version conflict")
)

type ReferenceInput struct {
	PermissionID string `json:"permission_id"`
	Scope        string `json:"scope"`
}

type SetInput struct {
	RouteID     string
	Expression  string
	Description string
	Status      string
	Version     int64
	References  []ReferenceInput
}

type Record struct {
	ID          string    `db:"id" json:"id"`
	RouteID     string    `db:"route_id" json:"route_id"`
	Expression  string    `db:"expression" json:"expression"`
	Description string    `db:"description" json:"description"`
	Priority    int64     `db:"priority" json:"priority"`
	Status      string    `db:"status" json:"status"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	CreatedBy   string    `db:"created_by" json:"created_by"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy   string    `db:"updated_by" json:"updated_by"`
	Version     int64     `db:"version" json:"version"`
}

type Reference struct {
	ID             string `db:"id" json:"id"`
	PermissionID   string `db:"permission_id" json:"permission_id"`
	PermissionKey  string `db:"permission_key" json:"permission_key"`
	PermissionName string `db:"permission_name" json:"permission_name"`
	Scope          string `db:"scope" json:"scope"`
}

type View struct {
	Record
	References []Reference `json:"references"`
}

type RouteSummary struct {
	ID               string    `db:"id" json:"id"`
	Protocol         string    `db:"protocol" json:"protocol"`
	Method           string    `db:"method" json:"method"`
	Path             string    `db:"path" json:"path"`
	Operation        string    `db:"operation" json:"operation"`
	Status           string    `db:"status" json:"status"`
	SourceVersion    string    `db:"source_version" json:"source_version"`
	LastDiscoveredAt time.Time `db:"last_discovered_at" json:"last_discovered_at"`
	PolicyExpression *string   `db:"policy_expression" json:"policy_expression"`
	PolicyStatus     *string   `db:"policy_status" json:"policy_status"`
	PolicyVersion    *int64    `db:"policy_version" json:"policy_version"`
}

type PageInput struct {
	pagination.Request
	Protocols []string
	Statuses  []string
}

type Page struct {
	Items    []RouteSummary `json:"items"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Total    int64          `json:"total"`
}

type Service struct {
	db         *sqlx.DB
	transactor *database.Transactor
	compiler   *Compiler
	manager    *Manager
	operations operationlog.Recorder
	security   securitylog.Recorder
	logger     *slog.Logger
}

func NewService(db *sqlx.DB, transactor *database.Transactor, compiler *Compiler, manager *Manager, operations operationlog.Recorder, security securitylog.Recorder, logger *slog.Logger) *Service {
	return &Service{db: db, transactor: transactor, compiler: compiler, manager: manager, operations: operations, security: security, logger: logger}
}

func (s *Service) Get(ctx context.Context, routeID string) (View, error) {
	var record Record
	query := s.db.Rebind(`SELECT id,route_id,expression,description,priority,status,created_at,created_by,updated_at,updated_by,version FROM route_policy_definitions WHERE route_id=? AND deleted_at IS NULL`)
	if err := s.db.GetContext(ctx, &record, query, routeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return View{}, ErrNotFound
		}
		return View{}, fmt.Errorf("get route policy: %w", err)
	}
	references := []Reference{}
	refQuery := s.db.Rebind(`SELECT r.id,r.permission_id,p.permission_key,p.name AS permission_name,r.scope FROM route_policy_permission_refs r JOIN permissions p ON p.id=r.permission_id AND p.deleted_at IS NULL WHERE r.policy_id=? AND r.deleted_at IS NULL ORDER BY p.permission_key`)
	if err := s.db.SelectContext(ctx, &references, refQuery, record.ID); err != nil {
		return View{}, fmt.Errorf("list route policy references: %w", err)
	}
	return View{Record: record, References: references}, nil
}

func (s *Service) Page(ctx context.Context, input PageInput) (Page, error) {
	request, err := pagination.Normalize(input.Request)
	if err != nil || len(input.Protocols) > 20 || len(input.Statuses) > 20 {
		return Page{}, ErrInvalid
	}
	where := []string{"r.deleted_at IS NULL"}
	args := []any{}
	if keyword := strings.TrimSpace(request.Keyword); keyword != "" {
		where = append(where, `(LOWER(r.path) LIKE ? OR LOWER(r.operation) LIKE ? OR LOWER(r.description) LIKE ?)`)
		like := "%" + strings.ToLower(keyword) + "%"
		args = append(args, like, like, like)
	}
	if len(input.Protocols) > 0 {
		clause, values, inErr := sqlx.In(`r.protocol IN (?)`, input.Protocols)
		if inErr != nil {
			return Page{}, fmt.Errorf("build protocol filter: %w", inErr)
		}
		where = append(where, clause)
		args = append(args, values...)
	}
	if len(input.Statuses) > 0 {
		clause, values, inErr := sqlx.In(`r.status IN (?)`, input.Statuses)
		if inErr != nil {
			return Page{}, fmt.Errorf("build status filter: %w", inErr)
		}
		where = append(where, clause)
		args = append(args, values...)
	}
	from := ` FROM route_definitions r LEFT JOIN route_policy_definitions p ON p.route_id=r.id AND p.deleted_at IS NULL WHERE ` + strings.Join(where, " AND ")
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*)`+from), args...); err != nil {
		return Page{}, fmt.Errorf("count route definitions: %w", err)
	}
	items := []RouteSummary{}
	query := `SELECT r.id,r.protocol,r.method,r.path,r.operation,r.status,r.source_version,r.last_discovered_at,p.expression AS policy_expression,p.status AS policy_status,p.version AS policy_version` + from + ` ORDER BY r.last_discovered_at DESC,r.id LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(query), pageArgs...); err != nil {
		return Page{}, fmt.Errorf("page route definitions: %w", err)
	}
	return Page{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *Service) Set(ctx context.Context, input SetInput) (View, error) {
	var record View
	err := operationlog.Do(ctx, s.operations, operationlog.Entry{
		Operation: "route_policy.set", ResourceType: "route_policy", ResourceID: input.RouteID,
		Source: "backend", Protocol: "http", Request: map[string]any{"status": input.Status, "version": input.Version},
	}, func() error {
		var setErr error
		record, setErr = s.set(ctx, input)
		return setErr
	})
	securityErr := s.security.Record(ctx, securitylog.Entry{
		EventType: securitylog.EventRoutePolicyChanged, SubjectID: input.RouteID,
		SubjectType: "route_policy", Succeeded: err == nil, Metadata: map[string]any{"status": input.Status, "version": input.Version},
	})
	if err == nil && securityErr != nil && s.security.FailClosed() {
		return View{}, securityErr
	}
	return record, err
}

func (s *Service) set(ctx context.Context, input SetInput) (View, error) {
	principal, ok := platformprincipal.FromContext(ctx)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return View{}, platformprincipal.ErrMissing
	}
	if input.RouteID == "" || (input.Status != "active" && input.Status != "disabled") || input.Version < 0 {
		return View{}, ErrInvalid
	}
	permissions, err := s.resolvePermissions(ctx, input.References)
	if err != nil {
		return View{}, err
	}
	if _, err := s.compiler.Compile(Definition{Expression: input.Expression, Permissions: permissions}); err != nil {
		return View{}, err
	}
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		if err := ensureRoute(ctx, tx, input.RouteID); err != nil {
			return err
		}
		if err := setPolicy(ctx, tx, principal.ID, input); err != nil {
			return err
		}
		return syncReferences(ctx, tx, principal.ID, input.RouteID, input.References)
	})
	if err != nil {
		return View{}, err
	}
	if err := s.manager.Refresh(ctx); err != nil {
		return View{}, fmt.Errorf("refresh local route policy: %w", err)
	}
	if err := s.manager.Notify(ctx); err != nil {
		s.logger.WarnContext(ctx, "notify route policy cache refresh", "error", err)
	}
	return s.Get(ctx, input.RouteID)
}

func (s *Service) resolvePermissions(ctx context.Context, refs []ReferenceInput) (map[string]Permission, error) {
	permissions := make(map[string]Permission, len(refs))
	seenIDs := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref.PermissionID == "" {
			return nil, ErrInvalid
		}
		if _, err := parseScope(ref.Scope); err != nil {
			return nil, err
		}
		if _, exists := seenIDs[ref.PermissionID]; exists {
			return nil, fmt.Errorf("%w: duplicate permission reference", ErrInvalid)
		}
		seenIDs[ref.PermissionID] = struct{}{}
	}
	if len(refs) == 0 {
		return permissions, nil
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.PermissionID)
	}
	query, args, err := sqlx.In(`SELECT id,permission_key,resource,action FROM permissions WHERE id IN (?) AND node_type='permission' AND status='active' AND deleted_at IS NULL`, ids)
	if err != nil {
		return nil, fmt.Errorf("build permission reference query: %w", err)
	}
	rows := []struct {
		ID       string `db:"id"`
		Key      string `db:"permission_key"`
		Resource string `db:"resource"`
		Action   string `db:"action"`
	}{}
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(query), args...); err != nil {
		return nil, fmt.Errorf("select permission references: %w", err)
	}
	if len(rows) != len(refs) {
		return nil, fmt.Errorf("%w: permission reference is missing or inactive", ErrInvalid)
	}
	byID := make(map[string]ReferenceInput, len(refs))
	for _, ref := range refs {
		byID[ref.PermissionID] = ref
	}
	for _, row := range rows {
		scope, _ := parseScope(byID[row.ID].Scope)
		permissions[row.Key] = Permission{ID: row.ID, Key: row.Key, Resource: row.Resource, Action: row.Action, Scope: scope}
	}
	return permissions, nil
}

func ensureRoute(ctx context.Context, tx *sqlx.Tx, routeID string) error {
	var exists int
	if err := tx.GetContext(ctx, &exists, tx.Rebind(`SELECT 1 FROM route_definitions WHERE id=? AND status='active' AND deleted_at IS NULL`), routeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("get route definition: %w", err)
	}
	return nil
}

func setPolicy(ctx context.Context, tx *sqlx.Tx, actor string, input SetInput) error {
	now := time.Now()
	if input.Version == 0 {
		query := tx.Rebind(`INSERT INTO route_policy_definitions(id,route_id,expression,description,priority,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,0,?,?,?,?,?,1)`)
		if _, err := tx.ExecContext(ctx, query, input.RouteID, input.RouteID, input.Expression, input.Description, input.Status, now, actor, now, actor); err != nil {
			return fmt.Errorf("create route policy: %w", err)
		}
		return nil
	}
	query := tx.Rebind(`UPDATE route_policy_definitions SET expression=?,description=?,status=?,updated_at=?,updated_by=?,version=version+1 WHERE route_id=? AND version=? AND deleted_at IS NULL`)
	result, err := tx.ExecContext(ctx, query, input.Expression, input.Description, input.Status, now, actor, input.RouteID, input.Version)
	if err != nil {
		return fmt.Errorf("update route policy: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read route policy update result: %w", err)
	}
	if affected != 1 {
		return ErrConflict
	}
	return nil
}

func syncReferences(ctx context.Context, tx *sqlx.Tx, actor, policyID string, refs []ReferenceInput) error {
	existing := []struct {
		ID           string       `db:"id"`
		PermissionID string       `db:"permission_id"`
		DeletedAt    sql.NullTime `db:"deleted_at"`
	}{}
	query := tx.Rebind(`SELECT id,permission_id,deleted_at FROM route_policy_permission_refs WHERE policy_id=?`)
	if err := tx.SelectContext(ctx, &existing, query, policyID); err != nil {
		return fmt.Errorf("select route policy references: %w", err)
	}
	desired := make(map[string]string, len(refs))
	for _, ref := range refs {
		desired[ref.PermissionID] = ref.Scope
	}
	now := time.Now()
	for _, row := range existing {
		scope, keep := desired[row.PermissionID]
		if keep {
			delete(desired, row.PermissionID)
			if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE route_policy_permission_refs SET scope=?,deleted_at=NULL,deleted_by=NULL,updated_at=?,updated_by=?,version=version+1 WHERE id=?`), scope, now, actor, row.ID); err != nil {
				return fmt.Errorf("update route policy reference: %w", err)
			}
			continue
		}
		if row.DeletedAt.Valid {
			continue
		}
		if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE route_policy_permission_refs SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND deleted_at IS NULL`), now, actor, now, actor, row.ID); err != nil {
			return fmt.Errorf("delete route policy reference: %w", err)
		}
	}
	for permissionID, scope := range desired {
		query := tx.Rebind(`INSERT INTO route_policy_permission_refs(id,policy_id,permission_id,scope,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,1)`)
		if _, err := tx.ExecContext(ctx, query, uuid.NewString(), policyID, permissionID, scope, now, actor, now, actor); err != nil {
			return fmt.Errorf("create route policy reference: %w", err)
		}
	}
	return nil
}
