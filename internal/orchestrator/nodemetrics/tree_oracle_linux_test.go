//go:build linux

package nodemetrics

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func nativeCPUPrecision(t *testing.T) time.Duration {
	t.Helper()
	output, err := exec.Command("getconf", "CLK_TCK").Output()
	if err != nil {
		t.Fatal(err)
	}
	frequency, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || frequency == 0 {
		t.Fatalf("getconf CLK_TCK %q: %v", output, err)
	}
	return time.Duration(2*uint64(time.Second)/frequency) + 2*time.Microsecond
}

func nativeZombieOracle(t *testing.T, pid int) bool {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "State:" {
			return fields[1] == "Z"
		}
	}
	t.Fatal("process status omitted state")
	return false
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
