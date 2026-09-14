// Package cgroupcpu reads the cpu limit the kernel gives this process's
// container. A cloud runner in a pod is given a slice of a much larger
// machine, so this is the cpu it actually has and the figure a metered claim
// reports for itself.
package cgroupcpu

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Limit returns the cpu cores this container may use and whether the kernel
// declares a limit at all. A process the kernel caps at nothing, which is a
// laptop or a pod that set no cpu limit, reports false.
func Limit() (float64, bool) { return LimitUnder("/sys/fs/cgroup") }

// LimitUnder reads the limit from a cgroup filesystem rooted at dir, which is
// what lets a test hand it a tree of its own.
func LimitUnder(dir string) (float64, bool) {
	if cores, ok := limitV2(dir); ok {
		return cores, true
	}
	return limitV1(dir)
}

// safety: cgroup v2 spells an uncapped container "max", which is a limit only
// in the sense that there is none.
func limitV2(dir string) (float64, bool) {
	fields := strings.Fields(readTrim(filepath.Join(dir, "cpu.max")))
	if len(fields) != 2 || fields[0] == "max" {
		return 0, false
	}
	return quotaCores(fields[0], fields[1])
}

func limitV1(dir string) (float64, bool) {
	for _, controller := range []string{"cpu", "cpu,cpuacct"} {
		base := filepath.Join(dir, controller)
		cores, ok := quotaCores(
			readTrim(filepath.Join(base, "cpu.cfs_quota_us")),
			readTrim(filepath.Join(base, "cpu.cfs_period_us")))
		if ok {
			return cores, true
		}
	}
	return 0, false
}

func quotaCores(quota, period string) (float64, bool) {
	q, qerr := strconv.ParseInt(quota, 10, 64)
	p, perr := strconv.ParseInt(period, 10, 64)
	if qerr != nil || perr != nil || q <= 0 || p <= 0 {
		return 0, false
	}
	return float64(q) / float64(p), true
}

func readTrim(path string) string {
	raw, err := os.ReadFile(path) // #nosec G304 -- a path this package builds from a cgroup root
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
