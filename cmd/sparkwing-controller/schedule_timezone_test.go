package main

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
)

func TestScheduleTimeZonesWithEmptyZoneInfo(t *testing.T) {
	t.Setenv("ZONEINFO", t.TempDir())
	for _, zone := range []string{"America/Boise", "Etc/UTC"} {
		if err := (cronspec.Override{TZ: &zone}).Validate(); err != nil {
			t.Errorf("schedule time zone %q: %v", zone, err)
		}
	}
}
