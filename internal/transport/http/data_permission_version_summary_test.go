package httptransport

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/datapermission"
)

func TestSummarizeDataPermissionVersions(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, time.October, 15, 10, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	page := datapermission.VersionPage{
		Items: []datapermission.VersionRecord{{
			ID: "version-1", PolicyID: "policy-1", VersionNumber: 2,
			Document: "secret policy condition", Status: datapermission.VersionPublished,
			PublishedAt:     sql.NullTime{Time: created, Valid: true},
			PublishedBy:     sql.NullString{String: "publisher-1", Valid: true},
			PublishedByName: "发布人", CreatedAt: created, CreatedBy: "creator-1",
			CreatedByName: "创建人", UpdatedAt: created, UpdatedBy: "editor-1",
			UpdatedByName: "编辑人", Version: 3,
		}},
		Page: 1, PageSize: 20, Total: 1,
	}

	encoded, err := json.Marshal(summarizeDataPermissionVersions(page))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "document") || strings.Contains(string(encoded), page.Items[0].Document) {
		t.Fatalf("version list disclosed a policy document: %s", encoded)
	}
	var result DataPermissionVersionSummaryPage
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if result.Page != 1 || result.PageSize != 20 || result.Total != 1 || len(result.Items) != 1 {
		t.Fatalf("page metadata changed: %+v", result)
	}
	item := result.Items[0]
	if item.ID != "version-1" || item.PolicyID != "policy-1" || item.Status != datapermission.VersionPublished || item.VersionNumber != 2 || item.Version != 3 || item.CreatedByName != "创建人" || item.PublishedByName != "发布人" || item.PublishedAt == nil || item.PublishedBy == nil || *item.PublishedBy != "publisher-1" {
		t.Fatalf("version metadata changed: %+v", item)
	}
}

func TestSummarizeDataPermissionVersionsEmptyItems(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(summarizeDataPermissionVersions(datapermission.VersionPage{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"items":[]`) {
		t.Fatalf("empty version page must contain a non-null items array: %s", encoded)
	}
}
