package application

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestNavigationTarget(t *testing.T) {
	for _, test := range []struct {
		name    string
		id      string
		row     bool
		wantErr error
	}{
		{name: "existing application", id: " app-1 ", row: true},
		{name: "missing application", id: "app-2", wantErr: ErrNotFound},
		{name: "empty id", id: " ", wantErr: ErrInvalid},
		{name: "oversized id", id: strings.Repeat("x", 129), wantErr: ErrInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			service := New(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
			if test.wantErr != ErrInvalid {
				query := mock.ExpectQuery(`SELECT id,name FROM applications WHERE id=\? AND deleted_at IS NULL`).WithArgs(strings.TrimSpace(test.id))
				if test.row {
					query.WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow("app-1", "业务应用"))
				} else {
					query.WillReturnError(sql.ErrNoRows)
				}
			}
			ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "platform-user", Type: platformprincipal.TypeUser})
			got, err := service.NavigationTarget(ctx, test.id)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, NavigationTarget{ID: "app-1", Name: "业务应用"}, got)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
