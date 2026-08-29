package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

var (
	ErrNotFound = errors.New("user not found")
	ErrConflict = errors.New("user already exists")
	ErrVersion  = errors.New("user version conflict")
)

type Repository interface {
	Create(context.Context, sqlx.ExtContext, User) error
	Get(context.Context, string) (User, error)
	List(context.Context, int, int) ([]User, int64, error)
	Update(context.Context, User) error
	Delete(context.Context, string, int64) error
}

type SQLRepository struct{ db *sqlx.DB }

func NewRepository(db *sqlx.DB) Repository { return &SQLRepository{db: db} }

const columns = "id, name, email, version, created_at, updated_at"

func (r *SQLRepository) Create(ctx context.Context, exec sqlx.ExtContext, user User) error {
	if r.db == nil {
		return fmt.Errorf("create user: database is disabled")
	}
	query := r.db.Rebind("INSERT INTO users (id, name, email, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)")
	if _, err := exec.ExecContext(ctx, query, user.ID, user.Name, user.Email, user.Version, user.CreatedAt, user.UpdatedAt); err != nil {
		if uniqueViolation(err) {
			return fmt.Errorf("%w: email", ErrConflict)
		}
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func (r *SQLRepository) Get(ctx context.Context, id string) (User, error) {
	if r.db == nil {
		return User{}, fmt.Errorf("get user: database is disabled")
	}
	var user User
	if err := r.db.GetContext(ctx, &user, r.db.Rebind("SELECT "+columns+" FROM users WHERE id = ?"), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("select user: %w", err)
	}
	return user, nil
}

func (r *SQLRepository) List(ctx context.Context, limit, offset int) ([]User, int64, error) {
	if r.db == nil {
		return nil, 0, fmt.Errorf("list users: database is disabled")
	}
	var total int64
	if err := r.db.GetContext(ctx, &total, "SELECT COUNT(*) FROM users"); err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}
	users := make([]User, 0)
	if err := r.db.SelectContext(ctx, &users, r.db.Rebind("SELECT "+columns+" FROM users ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?"), limit, offset); err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	return users, total, nil
}

func (r *SQLRepository) Update(ctx context.Context, user User) error {
	if r.db == nil {
		return fmt.Errorf("update user: database is disabled")
	}
	result, err := r.db.ExecContext(ctx, r.db.Rebind("UPDATE users SET name = ?, email = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ?"), user.Name, user.Email, user.UpdatedAt, user.ID, user.Version)
	if err != nil {
		if uniqueViolation(err) {
			return fmt.Errorf("%w: email", ErrConflict)
		}
		return fmt.Errorf("update user: %w", err)
	}
	return affected(result)
}

func (r *SQLRepository) Delete(ctx context.Context, id string, version int64) error {
	if r.db == nil {
		return fmt.Errorf("delete user: database is disabled")
	}
	result, err := r.db.ExecContext(ctx, r.db.Rebind("DELETE FROM users WHERE id = ? AND version = ?"), id, version)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return affected(result)
}

func affected(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows: %w", err)
	}
	if count == 0 {
		return ErrVersion
	}
	return nil
}

func uniqueViolation(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return strings.Contains(strings.ToLower(err.Error()), "duplicate key")
}
