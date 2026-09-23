package main

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
)

// A logs service with an archive prunes it after 30 days unless the operator
// named a retention, zero included; without an archive nothing changes.
func TestAnArchivedLogsServiceDefaultsToThirtyDaysOfRetention(t *testing.T) {
	for _, c := range []struct {
		name      string
		retention time.Duration
		named     bool
		archive   string
		want      time.Duration
	}{
		{"archive, unnamed", 0, false, "s3://b/logs", logs.DefaultArchiveRetention},
		{"archive, named zero", 0, true, "s3://b/logs", 0},
		{"archive, named a week", 168 * time.Hour, true, "s3://b/logs", 168 * time.Hour},
		{"no archive", 0, false, "", 0},
	} {
		if got := archiveRetention(c.retention, c.named, c.archive); got != c.want {
			t.Errorf("%s = %s, want %s", c.name, got, c.want)
		}
	}
}
