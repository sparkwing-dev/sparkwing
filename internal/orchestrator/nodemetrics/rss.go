package nodemetrics

import (
	"strconv"
	"strings"
)

// parseProcessRSSKB converts a `ps -o rss=` reading, which ps reports in
// kilobytes, into bytes. It reports false for output carrying no parsable
// row, so a ps that could not read the process table never passes as a
// process holding no memory and the caller's MemStats fallback applies.
func parseProcessRSSKB(out string) (int64, bool) {
	for _, line := range strings.Split(out, "\n") {
		field := strings.TrimSpace(line)
		if field == "" {
			continue
		}
		kb, err := strconv.ParseInt(field, 10, 64)
		if err != nil || kb <= 0 {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
