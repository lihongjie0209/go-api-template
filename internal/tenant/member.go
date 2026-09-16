package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/outbound"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	identityv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/identity/v1"
)

var ErrIdentityUnavailable = errors.New("identity service unavailable")

type UserSnapshot struct{ ID, Username, DisplayName string }
type UserResolver interface {
	ResolveUsername(context.Context, string) (UserSnapshot, error)
}
type UserDisplayResolver interface {
	ResolveUserIDs(context.Context, []string) (map[string]string, error)
}
type grpcUserResolver struct {
	client identityv1.IdentityServiceClient
}

func NewUserResolver(registry *outbound.Registry) UserResolver {
	conn, ok := registry.GRPC("identity")
	if !ok {
		return &grpcUserResolver{}
	}
	return &grpcUserResolver{client: identityv1.NewIdentityServiceClient(conn)}
}
func (r *grpcUserResolver) ResolveUsername(ctx context.Context, username string) (UserSnapshot, error) {
	if r.client == nil {
		return UserSnapshot{}, ErrIdentityUnavailable
	}
	response, err := r.client.ListUsers(ctx, &identityv1.ListUsersRequest{Keyword: "=" + username, Status: identityv1.UserStatus_USER_STATUS_ACTIVE, Page: &commonv1.PageRequest{Page: 1, PageSize: 1}})
	if err != nil {
		return UserSnapshot{}, fmt.Errorf("%w: %v", ErrIdentityUnavailable, err)
	}
	if len(response.GetUsers()) == 1 && strings.EqualFold(response.GetUsers()[0].GetUsername(), username) {
		u := response.GetUsers()[0]
		return UserSnapshot{ID: u.GetId(), Username: u.GetUsername(), DisplayName: u.GetDisplayName()}, nil
	}
	return UserSnapshot{}, ErrNotFound
}

func (r *grpcUserResolver) ResolveUserIDs(ctx context.Context, ids []string) (map[string]string, error) {
	if r.client == nil {
		return nil, ErrIdentityUnavailable
	}
	names := make(map[string]string, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if len(id) > 256 {
			return nil, ErrInvalid
		}
		if _, exists := names[id]; exists {
			continue
		}
		names[id] = id
		unique = append(unique, id)
		if len(unique) > 200 {
			return nil, ErrInvalid
		}
	}
	if len(unique) == 0 {
		return names, nil
	}
	response, err := r.client.BatchGetUsers(ctx, &identityv1.BatchGetUsersRequest{UserIds: unique})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIdentityUnavailable, err)
	}
	for _, user := range response.GetUsers() {
		if _, requested := names[user.GetId()]; requested && strings.TrimSpace(user.GetDisplayName()) != "" {
			names[user.GetId()] = user.GetDisplayName()
		}
	}
	return names, nil
}

type Member struct {
	ID            string    `db:"id" json:"id"`
	TenantID      string    `db:"tenant_id" json:"tenant_id"`
	UserID        string    `db:"user_id" json:"user_id"`
	Username      string    `db:"username" json:"username"`
	DisplayName   string    `db:"display_name" json:"display_name"`
	Status        Status    `db:"status" json:"status"`
	JoinedAt      time.Time `db:"joined_at" json:"joined_at"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
	CreatedBy     string    `db:"created_by" json:"created_by"`
	CreatedByName string    `db:"-" json:"created_by_name"`
	UpdatedAt     time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy     string    `db:"updated_by" json:"updated_by"`
	UpdatedByName string    `db:"-" json:"updated_by_name"`
	Version       int64     `db:"version" json:"version"`
}
type MemberPageInput struct {
	pagination.Request
	IDs, UserIDs, Usernames []string
	Statuses                []Status
	JoinedFrom, JoinedTo    *time.Time
}

const memberColumns = `id,tenant_id,user_id,username,display_name,status,joined_at,created_at,created_by,updated_at,updated_by,version`

func (r *Repository) GetMember(ctx context.Context, tenantID, id string) (Member, error) {
	if tenantID == "" {
		return Member{}, ErrForbidden
	}
	var v Member
	err := r.db.GetContext(ctx, &v, r.db.Rebind(`SELECT `+memberColumns+` FROM tenant_memberships WHERE tenant_id=? AND id=? AND deleted_at IS NULL`), tenantID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, fmt.Errorf("get member: %w", err)
	}
	return v, nil
}
func (r *Repository) PageMembers(ctx context.Context, tenantID string, input MemberPageInput) ([]Member, int64, error) {
	if tenantID == "" {
		return nil, 0, ErrForbidden
	}
	where := "tenant_id=? AND deleted_at IS NULL"
	args := []any{tenantID}
	if input.Keyword != "" {
		where += " AND (LOWER(username) LIKE ? OR LOWER(display_name) LIKE ?)"
		p := "%" + strings.ToLower(input.Keyword) + "%"
		args = append(args, p, p)
	}
	filters := []struct {
		sql string
		v   any
		n   int
	}{{"id IN (?)", input.IDs, len(input.IDs)}, {"user_id IN (?)", input.UserIDs, len(input.UserIDs)}, {"LOWER(username) IN (?)", lower(input.Usernames), len(input.Usernames)}, {"status IN (?)", input.Statuses, len(input.Statuses)}}
	for _, f := range filters {
		if f.n > 0 {
			where += " AND " + f.sql
			args = append(args, f.v)
		}
	}
	if input.JoinedFrom != nil {
		where += " AND joined_at>=?"
		args = append(args, *input.JoinedFrom)
	}
	if input.JoinedTo != nil {
		where += " AND joined_at<?"
		args = append(args, *input.JoinedTo)
	}
	var err error
	where, args, err = sqlx.In(where, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("build member filters: %w", err)
	}
	var total int64
	if err := r.db.GetContext(ctx, &total, r.db.Rebind(`SELECT count(*) FROM tenant_memberships WHERE `+where), args...); err != nil {
		return nil, 0, err
	}
	qargs := append(append([]any{}, args...), input.PageSize, pagination.Offset(input.Request))
	rows := []Member{}
	err = r.db.SelectContext(ctx, &rows, r.db.Rebind(`SELECT `+memberColumns+` FROM tenant_memberships WHERE `+where+` ORDER BY joined_at DESC,id LIMIT ? OFFSET ?`), qargs...)
	return rows, total, err
}
func lower(v []string) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = strings.ToLower(v[i])
	}
	return out
}

type MembershipService struct {
	repository *Repository
	transactor *database.Transactor
	users      UserResolver
	locker     cache.Locker
	cfg        config.Config
	operations operationlog.TransactionalRecorder
	security   securitylog.TransactionalRecorder
}

func NewMembershipService(r *Repository, t *database.Transactor, u UserResolver, l cache.Locker, c config.Config, operations operationlog.TransactionalRecorder, security securitylog.TransactionalRecorder) *MembershipService {
	return &MembershipService{repository: r, transactor: t, users: u, locker: l, cfg: c, operations: operations, security: security}
}
func (s *MembershipService) Add(ctx context.Context, username string) (Member, error) {
	actor, e := requireActor(ctx)
	if e != nil {
		return Member{}, e
	}
	username = strings.TrimSpace(username)
	if actor.TenantID == "" || username == "" || len(username) > 256 {
		return Member{}, ErrInvalid
	}
	user, e := s.users.ResolveUsername(ctx, username)
	if e != nil {
		return Member{}, e
	}
	id := uuid.NewString()
	now := time.Now()
	var created Member
	var businessErr error
	run := func(runCtx context.Context) error {
		businessErr = s.mutate(runCtx, "tenant.member.add", id, map[string]any{"username": username}, securitylog.Entry{EventType: securitylog.EventMembershipAdded, SubjectID: user.ID, SubjectType: "user", TenantID: actor.TenantID}, nil, func(tx *sqlx.Tx) error {
			if err := ensureActiveTenant(runCtx, tx, actor.TenantID); err != nil {
				return err
			}
			q := tx.Rebind(`INSERT INTO tenant_memberships (id,tenant_id,user_id,username,display_name,status,joined_at,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,?,?,?,?,1)`)
			_, err := tx.ExecContext(runCtx, q, id, actor.TenantID, user.ID, user.Username, user.DisplayName, StatusActive, now, now, actor.ID, now, actor.ID)
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		})
		if businessErr != nil {
			return businessErr
		}
		created, businessErr = s.repository.GetMember(runCtx, actor.TenantID, id)
		return businessErr
	}
	if s.locker == nil {
		if err := run(ctx); err != nil {
			return Member{}, err
		}
		return s.presentMember(ctx, created)
	}
	lockErr := cache.WithLock(ctx, s.locker, "tenant:"+actor.TenantID+":membership:"+user.ID, s.cfg.DistributedLock.TTL, s.cfg.DistributedLock.RetryDelay, run)
	if businessErr != nil {
		return Member{}, businessErr
	}
	if lockErr != nil {
		return Member{}, ErrConflict
	}
	return s.presentMember(ctx, created)
}

func (s *MembershipService) Get(ctx context.Context, id string) (Member, error) {
	actor, e := requireActor(ctx)
	if e != nil {
		return Member{}, e
	}
	id = strings.TrimSpace(id)
	if actor.TenantID == "" || id == "" || len(id) > maxTenantIDLength {
		return Member{}, ErrInvalid
	}
	member, err := s.repository.GetMember(ctx, actor.TenantID, id)
	if err != nil {
		return Member{}, err
	}
	return s.presentMember(ctx, member)
}
func (s *MembershipService) UpdateStatus(ctx context.Context, id string, status Status, version int64) (Member, error) {
	actor, e := requireActor(ctx)
	if e != nil {
		return Member{}, e
	}
	id = strings.TrimSpace(id)
	if actor.TenantID == "" || id == "" || len(id) > maxTenantIDLength || version <= 0 || (status != StatusActive && status != StatusDisabled) {
		return Member{}, ErrInvalid
	}
	e = s.mutate(ctx, "tenant.member.status.update", id, map[string]any{"status": status, "version": version}, securitylog.Entry{EventType: securitylog.EventMembershipChanged, SubjectID: id, SubjectType: "tenant_membership", TenantID: actor.TenantID, Metadata: map[string]any{"status": status}}, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
		if err := ensureActiveTenant(ctx, tx, actor.TenantID); err != nil {
			return err
		}
		if status == StatusDisabled {
			if err := protectLastAdministrator(ctx, tx, actor.TenantID, id); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_memberships SET status=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NULL`), status, time.Now(), actor.ID, actor.TenantID, id, version)
		if err != nil {
			return err
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("update membership affected rows: %w", rowsErr)
		}
		if rows != 1 {
			return ErrConflict
		}
		return nil
	})
	if e != nil {
		return Member{}, e
	}
	member, e := s.repository.GetMember(ctx, actor.TenantID, id)
	if e != nil {
		return Member{}, e
	}
	return s.presentMember(ctx, member)
}
func (s *MembershipService) Remove(ctx context.Context, id string, version int64) error {
	actor, e := requireActor(ctx)
	if e != nil {
		return e
	}
	id = strings.TrimSpace(id)
	if actor.TenantID == "" || id == "" || len(id) > maxTenantIDLength || version <= 0 {
		return ErrInvalid
	}
	e = s.mutate(ctx, "tenant.member.remove", id, map[string]any{"version": version}, securitylog.Entry{EventType: securitylog.EventMembershipRemoved, SubjectID: id, SubjectType: "tenant_membership", TenantID: actor.TenantID}, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
		if err := ensureActiveTenant(ctx, tx, actor.TenantID); err != nil {
			return err
		}
		if err := protectLastAdministrator(ctx, tx, actor.TenantID, id); err != nil {
			return err
		}
		now := time.Now()
		for _, table := range []string{"tenant_member_roles", "tenant_department_members", "tenant_administrators"} {
			if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE `+table+` SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND membership_id=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, actor.TenantID, id); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_memberships SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, actor.TenantID, id, version)
		if err != nil {
			return err
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("remove membership affected rows: %w", rowsErr)
		}
		if rows != 1 {
			return ErrConflict
		}
		return nil
	})
	return e
}
func ensureActiveTenant(ctx context.Context, tx *sqlx.Tx, tenantID string) error {
	var count int
	if err := tx.GetContext(ctx, &count, tx.Rebind(`SELECT count(*) FROM tenants WHERE id=? AND status='active' AND deleted_at IS NULL`), tenantID); err != nil {
		return err
	}
	if count != 1 {
		return ErrForbidden
	}
	return nil
}
func protectLastAdministrator(ctx context.Context, tx *sqlx.Tx, tenantID, membershipID string) error {
	var lockedTenantID string
	if err := tx.GetContext(ctx, &lockedTenantID, tx.Rebind(`SELECT id FROM tenants WHERE id=? AND deleted_at IS NULL FOR UPDATE`), tenantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrForbidden
		}
		return err
	}
	var isAdmin, count int
	if err := tx.GetContext(ctx, &isAdmin, tx.Rebind(`SELECT count(*) FROM tenant_administrators WHERE tenant_id=? AND membership_id=? AND deleted_at IS NULL`), tenantID, membershipID); err != nil {
		return err
	}
	if isAdmin == 0 {
		return nil
	}
	query := `SELECT count(*) FROM tenant_administrators a JOIN tenant_memberships m ON m.tenant_id=a.tenant_id AND m.id=a.membership_id AND m.status='active' AND m.deleted_at IS NULL WHERE a.tenant_id=? AND a.deleted_at IS NULL`
	if err := tx.GetContext(ctx, &count, tx.Rebind(query), tenantID); err != nil {
		return err
	}
	if count <= 1 {
		return ErrConflict
	}
	return nil
}
func (s *MembershipService) mutate(ctx context.Context, operation, id string, request any, securityEntry securitylog.Entry, options *sql.TxOptions, fn func(*sqlx.Tx) error) error {
	started := time.Now()
	operationEntry := operationlog.Entry{Operation: operation, ResourceType: "tenant_membership", ResourceID: id, Source: "backend", Protocol: "service", Request: request}
	err := s.transactor.Within(ctx, options, func(tx *sqlx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		operationEntry.Duration = time.Since(started)
		operationEntry.Succeeded = true
		if s.operations != nil {
			if err := s.operations.RecordTx(ctx, tx, operationEntry); err != nil {
				return err
			}
		}
		securityEntry.Succeeded = true
		if s.security != nil {
			return s.security.RecordTx(ctx, tx, securityEntry)
		}
		return nil
	})
	if err != nil {
		operationEntry.Duration = time.Since(started)
		operationEntry.Succeeded = false
		operationEntry.ErrorCode = "operation_failed"
		operationEntry.ErrorMessage = "operation failed"
		if s.operations != nil {
			_ = s.operations.Record(ctx, operationEntry)
		}
		securityEntry.Succeeded = false
		securityEntry.ErrorCode = "operation_failed"
		securityEntry.ErrorMessage = "operation failed"
		if s.security != nil {
			_ = s.security.Record(ctx, securityEntry)
		}
	}
	return err
}
func (s *MembershipService) Page(ctx context.Context, input MemberPageInput) (pagination.Result[Member], error) {
	actor, e := requireActor(ctx)
	if e != nil {
		return pagination.Result[Member]{}, e
	}
	request, e := pagination.Normalize(input.Request)
	input.Keyword = strings.TrimSpace(input.Keyword)
	if e != nil || actor.TenantID == "" || len(input.Keyword) > maxTenantKeywordLength || len(input.IDs) > 200 || len(input.UserIDs) > 200 || len(input.Usernames) > 200 || len(input.Statuses) > 20 || !boundedStrings(input.IDs, maxTenantIDLength) || !boundedStrings(input.UserIDs, maxTenantIDLength) || !boundedStrings(input.Usernames, 256) || (input.JoinedFrom != nil && input.JoinedTo != nil && !input.JoinedFrom.Before(*input.JoinedTo)) {
		return pagination.Result[Member]{}, ErrInvalid
	}
	for _, status := range input.Statuses {
		if status != StatusActive && status != StatusDisabled {
			return pagination.Result[Member]{}, ErrInvalid
		}
	}
	input.Request = request
	items, total, e := s.repository.PageMembers(ctx, actor.TenantID, input)
	if e == nil {
		e = s.presentMembers(ctx, items)
	}
	return pagination.Result[Member]{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, e
}

func (s *MembershipService) presentMember(ctx context.Context, member Member) (Member, error) {
	members := []Member{member}
	if err := s.presentMembers(ctx, members); err != nil {
		return Member{}, err
	}
	return members[0], nil
}

func (s *MembershipService) presentMembers(ctx context.Context, members []Member) error {
	ids := make([]string, 0, len(members)*2)
	for _, member := range members {
		ids = append(ids, member.CreatedBy, member.UpdatedBy)
	}
	names := stableActorNames(ids)
	if resolver, ok := s.users.(UserDisplayResolver); ok {
		resolved, err := resolver.ResolveUserIDs(ctx, ids)
		if err != nil {
			return err
		}
		for id, name := range resolved {
			names[id] = name
		}
	}
	for index := range members {
		members[index].CreatedByName = names[members[index].CreatedBy]
		members[index].UpdatedByName = names[members[index].UpdatedBy]
		members[index].JoinedAt = presentation.Time(members[index].JoinedAt)
		members[index].CreatedAt = presentation.Time(members[index].CreatedAt)
		members[index].UpdatedAt = presentation.Time(members[index].UpdatedAt)
	}
	return nil
}

func stableActorNames(ids []string) map[string]string {
	names := make(map[string]string, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			names[id] = id
		}
	}
	return names
}
