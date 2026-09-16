package tenant

import (
	"testing"
	"time"
)

func TestToViewUsesAsiaShanghaiAndDisplayValues(t *testing.T) {
	t.Parallel()
	record := Record{
		ID: "tenant-1", Status: StatusActive, OwnerUserID: "user-1", OwnerName: "Alice",
		CreatedAt: time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC), CreatedByName: "Creator",
		UpdatedAt: time.Date(2026, time.September, 15, 1, 0, 0, 0, time.UTC), UpdatedByName: "Updater",
	}

	view := toView(record)
	if view.CreatedAt != "2026-09-15T08:00:00+08:00" {
		t.Fatalf("CreatedAt = %q", view.CreatedAt)
	}
	if view.StatusName != "启用" || view.Owner.Name != "Alice" {
		t.Fatalf("display values = %#v", view)
	}
	if view.CreatedByName != "Creator" || view.UpdatedByName != "Updater" {
		t.Fatalf("actor display values = %#v", view)
	}
}
