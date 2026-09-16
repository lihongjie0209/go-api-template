package tenant

import (
	"time"

	"github.com/lihongjie0209/go-api-template/internal/presentation"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

type Record struct {
	ID            string    `db:"id"`
	Code          string    `db:"code"`
	Name          string    `db:"name"`
	Description   string    `db:"description"`
	Status        Status    `db:"status"`
	OwnerUserID   string    `db:"owner_user_id"`
	OwnerName     string    `db:"owner_name"`
	CreatedAt     time.Time `db:"created_at"`
	CreatedBy     string    `db:"created_by"`
	CreatedByName string    `db:"-"`
	UpdatedAt     time.Time `db:"updated_at"`
	UpdatedBy     string    `db:"updated_by"`
	UpdatedByName string    `db:"-"`
	Version       int64     `db:"version"`
}

type View struct {
	ID            string    `json:"id"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Status        Status    `json:"status"`
	StatusName    string    `json:"status_name"`
	Owner         Reference `json:"owner"`
	CreatedAt     string    `json:"created_at"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	UpdatedAt     string    `json:"updated_at"`
	UpdatedBy     string    `json:"updated_by"`
	UpdatedByName string    `json:"updated_by_name"`
	Version       int64     `json:"version"`
}
type Reference struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func toView(record Record) View {
	return View{ID: record.ID, Code: record.Code, Name: record.Name, Description: record.Description, Status: record.Status, StatusName: statusName(record.Status), Owner: Reference{ID: record.OwnerUserID, Name: record.OwnerName}, CreatedAt: presentation.Time(record.CreatedAt).Format(time.RFC3339), CreatedBy: record.CreatedBy, CreatedByName: record.CreatedByName, UpdatedAt: presentation.Time(record.UpdatedAt).Format(time.RFC3339), UpdatedBy: record.UpdatedBy, UpdatedByName: record.UpdatedByName, Version: record.Version}
}
func statusName(status Status) string {
	if status == StatusActive {
		return "启用"
	}
	return "停用"
}
