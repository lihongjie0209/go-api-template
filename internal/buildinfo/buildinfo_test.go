package buildinfo

import (
	"testing"
	"time"
)

func TestCurrentIncludesBuildAndRuntimeMetadata(t *testing.T) {
	originalVersion, originalCommit, originalBuildTime := Version, Commit, BuildTime
	t.Cleanup(func() { Version, Commit, BuildTime = originalVersion, originalCommit, originalBuildTime })
	Version, Commit, BuildTime = "v1.2.3", "abc123", "2026-09-16T00:00:00Z"
	before := time.Now()
	info := Current()
	if info.Version != Version || info.Commit != Commit || info.BuildTime != BuildTime || info.StartedAt.After(before) || info.Uptime == "" {
		t.Fatalf("Current()=%+v", info)
	}
}
