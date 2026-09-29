//go:build linux

package procusage

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func nativeCPUPrecision(t *testing.T) time.Duration {
	t.Helper()
	frequency := clockTicks()
	if frequency == 0 {
		t.Fatal("kernel clock tick frequency unavailable")
	}
	return time.Duration(2*uint64(time.Second)/frequency) + 2*time.Microsecond
}

func nativeRSSOracle(t *testing.T, pid int) int64 {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/smaps")
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "Rss:" && fields[2] == "kB" {
			kib, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil || kib < 0 {
				t.Fatalf("smaps mapping RSS %q: %v", line, err)
			}
			total += kib * 1024
		}
	}
	if total <= 0 {
		t.Fatal("smaps did not establish resident memory")
	}
	return total
}
