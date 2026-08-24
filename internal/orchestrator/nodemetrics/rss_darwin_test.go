//go:build darwin

package nodemetrics

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestProcessRSS_ReadsPlausibleCurrentFootprint covers what the parser cannot:
// that the ps invocation still names the field and the process it means to,
// and that the reading is scaled into bytes. getrusage's lifetime peak is the
// available yardstick -- a current footprint sits under it, and a reading
// still in kilobytes would sit orders of magnitude under it.
func TestProcessRSS_ReadsPlausibleCurrentFootprint(t *testing.T) {
	rss, ok := processRSS()
	if !ok || rss <= 0 {
		t.Fatalf("processRSS() = %d, %t; want a positive current RSS", rss, ok)
	}

	var ru unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &ru); err != nil {
		t.Skipf("no getrusage yardstick: %v", err)
	}
	peak := ru.Maxrss
	if rss > peak {
		t.Errorf("current RSS %d exceeds the lifetime peak %d", rss, peak)
	}
	if rss < peak/100 {
		t.Errorf("current RSS %d is a hundredth of the peak %d; reading is not in bytes", rss, peak)
	}
}
