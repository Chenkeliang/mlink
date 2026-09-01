package version

import (
	"encoding/json"
	"testing"
)

func TestCurrentReportsBuildAndSchemaCompatibility(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, BuildDate
	defer func() { Version, Commit, BuildDate = oldVersion, oldCommit, oldDate }()
	Version, Commit, BuildDate = "1.2.3", "abc123", "2026-09-01T00:00:00Z"
	info := Current()
	if info.Version != "1.2.3" || info.Commit != "abc123" || info.BuildDate != "2026-09-01T00:00:00Z" || info.SchemaMin != 2 || info.SchemaMax != 3 || info.GOOS == "" || info.GOARCH == "" {
		t.Fatalf("Current() = %#v", info)
	}
	data, err := json.Marshal(info)
	if err != nil || string(data) == "{}" {
		t.Fatalf("Marshal() = %s, %v", data, err)
	}
}
