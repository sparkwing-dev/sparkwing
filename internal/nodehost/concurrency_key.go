package nodehost

import (
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const memoKeyPrefix = "memo:"

// ScopeLabelFromKey renders the scope and complete host identity of a concurrency key.
func ScopeLabelFromKey(key string) string {
	if strings.HasPrefix(key, memoKeyPrefix) {
		return "content-cache"
	}
	_, scope, identity, _, ok := runspec.ParseConcurrencyKey(key)
	if !ok {
		return "unknown"
	}
	switch scope {
	case "global":
		return "global"
	default:
		return scope + " (" + identity + ")"
	}
}
