//go:build darwin

package nodemetrics

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func nativeCPUPrecision(*testing.T) time.Duration { return 2 * time.Microsecond }

func nativeZombieOracle(t *testing.T, pid int) bool {
	t.Helper()
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		t.Fatal(err)
	}
	return process.Proc.P_stat == 5
}

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
