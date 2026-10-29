package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidSessionLookup = errors.New("invalid session lookup")

// Both bearer validation and the internal session probe must use the same
// authoritative account, credential, revocation, expiry, and deletion scope.
const activeUserSessionSource = `FROM identity_sessions s JOIN identity_users u ON u.id=s.user_id AND u.status='active' AND u.deleted_at IS NULL JOIN identity_user_credentials c ON c.user_id=s.user_id AND c.deleted_at IS NULL WHERE s.id=? AND s.user_id=? AND s.revoked_at IS NULL AND s.expires_at>? AND s.deleted_at IS NULL`

// ValidateSession checks authoritative state, never a potentially stale cache.
// An absent, revoked, expired, or disabled session is invalid rather than an error.
func (s *Service) ValidateSession(ctx context.Context, sessionID, userID string) (time.Time, bool, error) {
	if sessionID == "" || userID == "" || strings.TrimSpace(sessionID) != sessionID || strings.TrimSpace(userID) != userID || len(sessionID) > 256 || len(userID) > 256 {
		return time.Time{}, false, ErrInvalidSessionLookup
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, false, fmt.Errorf("%w: validate session: %w", ErrStateUnavailable, err)
	}
	if s == nil || s.db == nil {
		return time.Time{}, false, ErrStateUnavailable
	}
	var expiresAt time.Time
	query := s.db.Rebind(`SELECT s.expires_at ` + activeUserSessionSource)
	err := s.db.GetContext(ctx, &expiresAt, query, sessionID, userID, time.Now())
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("%w: validate session: %w", ErrStateUnavailable, err)
	}
	return expiresAt, true, nil
}
