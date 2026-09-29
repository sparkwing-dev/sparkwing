//go:build linux

package nodemetrics

import (
	"math"
	"os"
	"strconv"
	"strings"
)

func processRSS() (int64, bool) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	return parseProcessStatm(string(data))
}

func parseProcessStatm(data string) (int64, bool) {
	fields := strings.Fields(data)
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || pages < 0 || pages > math.MaxInt64/int64(os.Getpagesize()) {
		return 0, false
	}
	return pages * int64(os.Getpagesize()), true
}
