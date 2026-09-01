package nodemetrics

import (
	"strconv"
	"strings"
)

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
