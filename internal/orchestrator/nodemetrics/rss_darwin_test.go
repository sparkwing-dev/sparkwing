//go:build darwin

package nodemetrics

import (
	"testing"

	"golang.org/x/sys/unix"
)

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
