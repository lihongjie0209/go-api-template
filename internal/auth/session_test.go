package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
)

func TestService_ValidateSession(t *testing.T) {
	expiresAt := time.Date(2026, time.October, 22, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		rows      *sqlmock.Rows
		queryErr  error
		wantValid bool
		wantErr   error
	}{
		{name: "active session", rows: sqlmock.NewRows([]string{"expires_at"}).AddRow(expiresAt), wantValid: true},
		{name: "revoked expired or disabled session", rows: sqlmock.NewRows([]string{"expires_at"})},
		{name: "database unavailable", queryErr: errors.New("database unavailable"), wantErr: ErrStateUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			query := mock.ExpectQuery(`(?s)SELECT s.expires_at FROM identity_sessions s.*JOIN identity_users u.*JOIN identity_user_credentials c.*WHERE s.id=\? AND s.user_id=\?`)
			query.WithArgs("session-1", "user-1", sqlmock.AnyArg())
			if tt.queryErr != nil {
				query.WillReturnError(tt.queryErr)
			} else {
				query.WillReturnRows(tt.rows)
			}
			service := &Service{db: sqlx.NewDb(raw, "sqlmock")}
			gotExpiry, valid, err := service.ValidateSession(t.Context(), "session-1", "user-1")
			if !errors.Is(err, tt.wantErr) || valid != tt.wantValid {
				t.Fatalf("ValidateSession() = (%v, %v, %v), want valid=%v err=%v", gotExpiry, valid, err, tt.wantValid, tt.wantErr)
			}
			if tt.wantValid && !gotExpiry.Equal(expiresAt) {
				t.Fatalf("expiresAt = %v, want %v", gotExpiry, expiresAt)
			}
			if !tt.wantValid && !gotExpiry.IsZero() {
				t.Fatalf("invalid session exposed expiry %v", gotExpiry)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestService_ValidateSessionRejectsInvalidInputBeforeQuery(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, sessionID, userID string }{
		{"missing session", "", "user-1"},
		{"missing user", "session-1", ""},
		{"space padded session", " session-1", "user-1"},
		{"space padded user", "session-1", "user-1 "},
		{"oversized session", string(make([]byte, 257)), "user-1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, valid, err := (&Service{}).ValidateSession(t.Context(), tt.sessionID, tt.userID)
			if valid || !errors.Is(err, ErrInvalidSessionLookup) {
				t.Fatalf("valid=%v err=%v", valid, err)
			}
		})
	}
}

func TestService_ValidateSessionRequiresStateStore(t *testing.T) {
	t.Parallel()
	_, valid, err := (&Service{}).ValidateSession(context.Background(), "session-1", "user-1")
	if valid || !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("valid=%v err=%v", valid, err)
	}
}
