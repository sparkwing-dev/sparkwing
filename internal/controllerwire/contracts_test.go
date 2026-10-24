package controllerwire

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func TestCompatibilityUsesReleaseCalendarMonths(t *testing.T) {
	for _, published := range []time.Time{
		time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 14, 1, 2, 3, 0, time.UTC),
		time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		got := CompatibilityFor(engineid.VersionInfo{Version: "v1.2.3", PublishedAt: published, SupportUntil: published})
		if got.APIMajor != 1 || got.EngineRelease != "v1.2.3" || !got.ReleasedAt.Equal(published) || !got.GuaranteedUntil.Equal(published.AddDate(0, 12, 0)) {
			t.Fatalf("incorrect compatibility window: %#v", got)
		}
	}
}
