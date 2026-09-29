//go:build darwin

package procusage

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func nativeCPUPrecision(*testing.T) time.Duration { return 2 * time.Microsecond }

func nativeRSSOracle(t *testing.T, pid int) int64 {
	t.Helper()
	output, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatal(err)
	}
	kib, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || kib <= 0 {
		t.Fatalf("ps RSS %q: %v", output, err)
	}
	return kib * 1024
}
