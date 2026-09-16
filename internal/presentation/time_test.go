package presentation

import (
	"testing"
	"time"
)

func TestTimeUsesPlatformOffsetAndPreservesInstant(t *testing.T) {
	t.Parallel()
	input := time.Date(2026, time.September, 16, 1, 2, 3, 4, time.UTC)
	got := Time(input)
	if !got.Equal(input) || got.Format(time.RFC3339) != "2026-09-16T09:02:03+08:00" || got.Location().String() != "Asia/Shanghai" {
		t.Fatalf("Time() = %v (%s)", got, got.Location())
	}
	if got := Time(time.Time{}); !got.IsZero() {
		t.Fatalf("Time(zero) = %v, want zero", got)
	}
}
