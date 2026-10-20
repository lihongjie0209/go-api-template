package httptransport

import (
	"time"

	"github.com/lihongjie0209/go-api-template/internal/datapermission"
)

// DataPermissionVersionSummary excludes the policy document from list responses.
// The document is available only through the separately authorized get route.
type DataPermissionVersionSummary struct {
	ID              string     `json:"id"`
	PolicyID        string     `json:"policy_id"`
	VersionNumber   int64      `json:"version_number"`
	Status          string     `json:"status"`
	PublishedAt     *time.Time `json:"published_at"`
	PublishedBy     *string    `json:"published_by"`
	PublishedByName string     `json:"published_by_name"`
	CreatedAt       time.Time  `json:"created_at"`
	CreatedBy       string     `json:"created_by"`
	CreatedByName   string     `json:"created_by_name"`
	UpdatedAt       time.Time  `json:"updated_at"`
	UpdatedBy       string     `json:"updated_by"`
	UpdatedByName   string     `json:"updated_by_name"`
	Version         int64      `json:"version"`
}

type DataPermissionVersionSummaryPage struct {
	Items    []DataPermissionVersionSummary `json:"items"`
	Page     int                            `json:"page"`
	PageSize int                            `json:"page_size"`
	Total    int64                          `json:"total"`
}

func summarizeDataPermissionVersions(page datapermission.VersionPage) DataPermissionVersionSummaryPage {
	items := make([]DataPermissionVersionSummary, 0, len(page.Items))
	for _, version := range page.Items {
		var publishedAt *time.Time
		if version.PublishedAt.Valid {
			publishedAt = &version.PublishedAt.Time
		}
		var publishedBy *string
		if version.PublishedBy.Valid {
			publishedBy = &version.PublishedBy.String
		}
		items = append(items, DataPermissionVersionSummary{
			ID:              version.ID,
			PolicyID:        version.PolicyID,
			VersionNumber:   version.VersionNumber,
			Status:          version.Status,
			PublishedAt:     publishedAt,
			PublishedBy:     publishedBy,
			PublishedByName: version.PublishedByName,
			CreatedAt:       version.CreatedAt,
			CreatedBy:       version.CreatedBy,
			CreatedByName:   version.CreatedByName,
			UpdatedAt:       version.UpdatedAt,
			UpdatedBy:       version.UpdatedBy,
			UpdatedByName:   version.UpdatedByName,
			Version:         version.Version,
		})
	}
	return DataPermissionVersionSummaryPage{
		Items: items, Page: page.Page, PageSize: page.PageSize, Total: page.Total,
	}
}
