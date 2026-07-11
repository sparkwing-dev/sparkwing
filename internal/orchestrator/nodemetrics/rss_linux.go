//go:build linux

package nodemetrics

import (
	"os"
	"strconv"
	"strings"
)

// processRSS returns the process resident set size from /proc/self/statm,
// whose second field is the resident page count.
func processRSS() (int64, bool) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * int64(os.Getpagesize()), true
}
